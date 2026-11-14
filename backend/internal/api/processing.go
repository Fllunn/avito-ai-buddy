package api

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/agent"
	"ai-buddy/backend/internal/authz"
	"ai-buddy/backend/internal/execution"
	"ai-buddy/backend/internal/onboarding"
	"ai-buddy/backend/internal/storage"
	"ai-buddy/backend/internal/tools"
	"ai-buddy/backend/platform/calendar"
	"ai-buddy/backend/platform/demo"
	"ai-buddy/backend/platform/safety"
	"context"
	"encoding/json"
	"strings"
	"time"
)

// ProcessOnce runs message and refresh jobs; service mutations use actions.Worker.
func (b *Business) ProcessOnce(ctx context.Context, now time.Time) error {
	if e := b.Store.ExpireDrafts(ctx, b.Tools.Clock.Now()); e != nil {
		return e
	}
	if offered, e := b.Store.OfferNext(ctx, b.Tools.Clock.Now()); e != nil {
		return e
	} else if offered {
		return nil
	}
	if work, e := b.Store.ClaimRefresh(ctx, now, b.LLMTimeout+10*time.Second); e == nil {
		code := ""
		for _, service := range []string{"access", "time_off"} {
			items, e := b.Tools.Remote.List(ctx, work.Message.UserID, service)
			if e != nil {
				code = "SERVICE_UNAVAILABLE"
				continue
			}
			for _, object := range items {
				if e := b.Store.ApplyObject(ctx, work.Message.UserID, object, b.Tools.Clock.Now()); e != nil {
					code = "SERVICE_UNAVAILABLE"
				}
			}
		}
		return b.Store.CompleteRefresh(ctx, work, code)
	}
	work, e := b.Store.ClaimMessage(ctx, now, b.LLMTimeout+10*time.Second)
	if e != nil {
		return e
	}
	state := DialogueState{}
	raw, e := b.Store.Dialogue(ctx, work.Message.UserID)
	if e != nil {
		return e
	}
	if e = json.Unmarshal(raw, &state); e != nil {
		return e
	}
	state.DeclinedExecutions = nil
	state.Fallback = false
	text, result, code := b.process(ctx, work, &state)
	serialized, e := json.Marshal(state)
	if e != nil {
		return e
	}
	return b.Store.CompleteMessage(ctx, work, safety.Prepare(ctx, safety.Local{}, text), result, code, b.Tools.Clock.Now(), serialized)
}
func (b *Business) process(ctx context.Context, work storage.MessageWork, state *DialogueState) (string, any, string) {
	provider := &executionProvider{Provider: b.Provider, store: b.Store, work: work}
	_ = b.Store.ObserveMessage(ctx, work, "context", "running")
	frame, e := b.Builder.Build(ctx, work.Message.UserID)
	if e != nil {
		return "Не удалось проверить права. Попробуй ещё раз.", nil, "CONTEXT_UNAVAILABLE"
	}
	_ = b.Store.ObserveMessage(ctx, work, "context", "succeeded")
	if work.Message.FormRequired {
		if state.CardID != "" {
			state.Stage = "card_open"
		} else if state.Stage == "confirm_step" {
			state.Stage = ""
		}
		return "Причину и чувствительные сведения указывать не нужно. Продолжи через форму.", nil, ""
	}
	if state.Stage == "await_next" && strings.ToLower(strings.TrimSpace(work.Message.Text)) == "позже" {
		state.DeclinedExecutions = state.PendingExecutions
		state.PendingExecutions = nil
		state.Pending = nil
		state.Transfer = nil
		state.Stage = ""
		return "Второе действие отложено. Сохранённые запросы остаются без изменений.", nil, ""
	}
	state.LastIntent = "unclear"
	intents := []agent.Intent{}
	var inferred *agent.Intent
	defaulted := false
	if state.Current != nil && state.Current.Name == "access_request" && work.Message.Kind == "text" {
		q := strings.ToLower(strings.TrimSpace(work.Message.Text))
		if state.Stage == "outside_access" {
			if q == "сначала спрошу наставника" || q == "не надо" {
				state.Stage = ""
				state.Current = nil
				return "Хорошо. Уточни задачу у наставника; ничего не отправлено.", nil, ""
			}
			if q == "всё равно оформить" || q == "оформить для задачи" || q == "да" {
				copy := *state.Current
				state.OutsideAccepted = copy.Slots.ResourceID
				inferred = &copy
			}
		}
		if state.Stage == "parameters" && q == "не знаю" && state.Current.Slots.Level == "" {
			for _, r := range frame.Resources {
				if r.ID == state.Current.Slots.ResourceID {
					copy := *state.Current
					copy.Slots.Level = r.DefaultLevel
					inferred = &copy
					defaulted = true
					break
				}
			}
		}
	}
	if inferred == nil && state.Stage == "parameters" && state.Current != nil && work.Message.Kind == "text" {
		copy := *state.Current
		if mergeFollowup(&copy, work.Message.Text, b.Tools.Clock.Now(), b.Tools.Location) {
			inferred = &copy
		}
	}
	if inferred != nil {
		intents = []agent.Intent{*inferred}
	} else if work.Message.Kind == "button" {
		raw, _ := json.Marshal(work.Message.Slots)
		var slots agent.Slots
		_ = json.Unmarshal(raw, &slots)
		intents = []agent.Intent{{Name: work.Message.Intent, Slots: slots, Confidence: 1}}
	} else {
		recognized := (agent.Recognizer{Builder: b.Builder, Provider: provider, Timeout: b.LLMTimeout}).Recognize(ctx, work.Message.UserID, safety.Prepare(ctx, safety.Local{}, work.Message.Text))
		if recognized.Fallback {
			state.Fallback = true
			if provider.calls.Load() > 0 {
				_ = b.Store.ObserveMessage(ctx, work, "model", "failed", "fallback")
			}
			if state.Current != nil {
				slots := state.Current.Slots
				params := tools.Parameters{ResourceID: slots.ResourceID, Level: slots.Level, Justification: slots.Justification, StartDate: slots.StartDate, EndDate: slots.EndDate}
				field := "access_form"
				if state.Current.Name == "time_off_request" {
					field = "time_off_form"
				}
				return "Языковая модель недоступна. Собранные параметры сохранены; продолжи через форму.", map[string]any{"items": []any{map[string]any{field: params}}, "mode": recognized.Mode}, ""
			}
			return "Не удалось обработать свободный вопрос. Можно продолжить кнопками или повторить вопрос.", map[string]any{"code": recognized.Code, "mode": recognized.Mode}, ""
		}
		if recognized.Code == "CLARIFY" {
			return "Уточни, что нужно сделать. Доступны план, поиск инструкции, доступ и отгул.", nil, ""
		}
		intents = recognized.Data.Intents
	}
	if state.Stage == "outside_access" && inferred == nil && len(intents) == 1 && intents[0].Name != "access_request" {
		state.Stage = "parameters"
	}
	if state.Stage == "reapply" && len(intents) == 1 && intents[0].Name == "card_confirm" && state.Current != nil {
		copy := *state.Current
		copy.Slots.Query = reapplyQuery
		intents = []agent.Intent{copy}
	}
	targetRoute := ""
	if state.Stage == "await_next" && len(intents) == 1 && intents[0].Name == "card_cancel" && len(state.PendingExecutions) > 0 {
		targetRoute = state.PendingExecutions[0]
	}
	if len(intents) > 0 && state.ExecutionID != "" && state.Stage != "" {
		name := intents[0].Name
		if name == "card_confirm" || name == "card_edit" || name == "card_cancel" || inferred != nil || (state.Current != nil && name == state.Current.Name && state.Stage == "parameters") {
			if targetRoute == "" {
				targetRoute = state.ExecutionID
			}
		}
	}
	if state.Stage == "await_next" && len(intents) == 1 && intents[0].Name == "card_confirm" && len(state.Pending) > 0 {
		if len(state.PendingExecutions) > 0 {
			targetRoute = state.PendingExecutions[0]
			state.PendingExecutions = state.PendingExecutions[1:]
		} else {
			targetRoute = ""
		}
		intents = []agent.Intent{state.Pending[0]}
		state.Pending = state.Pending[1:]
		state.Current = nil
		state.CardID = ""
		state.Stage = ""
	}
	if len(intents) > 0 {
		scenario := executionScenario(intents[0].Name)
		route, err := b.Store.BindMessageRoute(ctx, work, targetRoute, scenario)
		if err != nil {
			return "Не удалось сохранить связь запроса.", nil, "STORAGE_UNAVAILABLE"
		}
		state.ExecutionID = route
		ctx = execution.WithRoute(ctx, route)
		observedWork := work
		observedWork.ExecutionID = route
		provider = &executionProvider{Provider: b.Provider, store: b.Store, work: observedWork}
	}
	if len(intents) > 1 {
		for pendingIndex, pending := range intents[1:] {
			id, err := b.Store.PendingExecution(ctx, work, executionScenario(pending.Name), int64(pendingIndex+1))
			if err != nil {
				return "Не удалось сохранить следующий запрос.", nil, "STORAGE_UNAVAILABLE"
			}
			state.PendingExecutions = append(state.PendingExecutions, id)
		}
		state.Pending = append(state.Pending, intents[1:]...)
		intents = intents[:1]
	}
	if len(intents) == 1 && (state.Stage == "confirm_step" || state.Stage == "reapply") {
		switch intents[0].Name {
		case "card_confirm", "card_cancel", "cancel", "access_request", "time_off_request":
		default:
			state.Stage = "parameters"
		}
	}
	if len(intents) > 0 && (intents[0].Name == "access_request" || intents[0].Name == "time_off_request") {
		copy := intents[0]
		state.Current = &copy
		state.Stage = "parameters"
	}
	if len(intents) == 1 && state.Stage == "confirm_card" {
		switch intents[0].Name {
		case "card_confirm", "card_edit", "card_cancel", "cancel":
		default:
			state.Stage = "card_open"
		}
	}
	if len(intents) > 0 {
		state.LastIntent = intents[0].Name
	}
	// Intent execution is deterministic. Text or model output never mutates a service.
	texts := []string{}
	if defaulted {
		texts = append(texts, "Выбран уровень по умолчанию из каталога; он указан в форме и карточке.")
	}
	results := []any{}
	for _, intent := range intents {
		switch intent.Name {
		case "next_step":
			plan := onboarding.Project(frame.Profile, frame.Steps, b.Tools.Clock.Now(), b.Tools.Location)
			texts = append(texts, plan.Notices...)
			if plan.Curator != nil {
				texts = append(texts, "Куратор — "+plan.Curator.Name+".")
			}
			if plan.ReadOnly {
				texts = append(texts, "Стажировка ещё не началась. План доступен только для чтения.")
			} else if len(plan.NextSteps) > 0 {
				texts = append(texts, "Ближайший шаг: «"+plan.NextSteps[0].Title+"». Остальные шаги — в панели онбординга.")
			} else {
				texts = append(texts, "Доступные шаги выполнены или ждут других участников. Проверь панель онбординга.")
			}
			results = append(results, map[string]any{"plan": plan})
		case "find_doc", "time_off_info":
			query := intent.Slots.Query
			if query == "" {
				query = work.Message.Text
			}
			if intent.Name == "time_off_info" {
				query = "отгул"
			}
			_ = b.Store.ObserveMessage(ctx, work, "sources", "running")
			answer := (agent.Answerer{Search: agent.Search{Builder: b.Builder}, Provider: provider, Timeout: b.LLMTimeout}).Answer(ctx, frame.Profile.ID, safety.Prepare(ctx, safety.Local{}, query))
			sourceState := "succeeded"
			if answer.Code == "SEARCH_UNAVAILABLE" || answer.Code == "CONTEXT_UNAVAILABLE" {
				sourceState = "failed"
			}
			_ = b.Store.ObserveMessage(ctx, provider.work, "sources", sourceState)
			if strings.HasPrefix(answer.Code, "LLM_") {
				state.Fallback = true
				if provider.calls.Load() > 0 {
					_ = b.Store.ObserveMessage(ctx, provider.work, "model", "failed", "fallback")
				}
			}
			texts = append(texts, answer.Text)
			results = append(results, map[string]any{"answer": answer})
		case "who_to_ask":
			topic := "buddy"
			query := strings.ToLower(intent.Slots.Query + " " + work.Message.Text)
			if strings.Contains(query, "куратор") || strings.Contains(query, "программ") {
				topic = "other"
			}
			if strings.Contains(query, "цел") {
				topic = "goals"
			}
			if intent.Slots.ResourceID != "" {
				topic = "resource"
			}
			reply := agent.ContactFor(frame, topic, intent.Slots.ResourceID)
			texts = append(texts, reply.Text)
			results = append(results, map[string]any{"contact": reply})
		case "access_request":
			if e := authz.ServiceAction(frame.Profile, b.Tools.Clock.Now(), b.Tools.Location); e != nil {
				texts = append(texts, "Сервисные действия доступны только стажёру после даты старта.")
				continue
			}
			params := tools.Parameters{ResourceID: intent.Slots.ResourceID, Level: intent.Slots.Level, Justification: intent.Slots.Justification}
			query := strings.ToLower(intent.Slots.Query + " " + work.Message.Text)
			matches := []string{}
			for _, r := range frame.Resources {
				matched := r.ID == params.ResourceID || tokenMatch(query, r.ID) || tokenMatch(query, r.Name)
				for _, alias := range r.Synonyms {
					if tokenMatch(query, alias) {
						matched = true
					}
				}
				if frame.Profile.TeamID != nil && strings.Contains(query, "моей команд") && r.ID == "space-"+*frame.Profile.TeamID {
					matched = true
				}
				if matched {
					matches = append(matches, r.ID)
				}
			}
			if params.ResourceID == "" && len(matches) == 1 {
				params.ResourceID = matches[0]
			}
			if params.ResourceID == "" {
				texts = append(texts, "Выбери ресурс в форме запроса доступа. Показываю только видимый каталог.")
				results = append(results, map[string]any{"resources": frame.Resources})
				continue
			}
			state.Current.Slots.ResourceID = params.ResourceID
			var found bool
			for _, r := range frame.Resources {
				if r.ID != params.ResourceID {
					continue
				}
				found = true
				if r.Kind == "closed" {
					reply := agent.ContactFor(frame, "resource", r.ID)
					texts = append(texts, reply.Text)
					results = append(results, map[string]any{"contact": reply})
					break
				}
				if stop := b.accessPreflight(ctx, frame.Profile.ID, r.ID); stop != "" {
					texts = append(texts, stop)
					break
				}
				if params.Level == "" && len(r.Levels) == 1 {
					params.Level = r.Levels[0]
				}
				prior, e := b.priorRejection(ctx, frame.Profile, "access_create", params)
				if e != nil {
					texts = append(texts, "Не удалось проверить предыдущие заявки. Карточка не создана.")
					break
				}
				if prior != nil && intent.Slots.Query != reapplyQuery && !(state.RejectedID == prior.ID && params.Justification != "") {
					state.RejectedID = prior.ID
					state.Current.Slots.ResourceID = params.ResourceID
					state.Current.Slots.Level = params.Level
					state.Current.Slots.Justification = ""
					state.Stage = "parameters"
					texts = append(texts, "Прошлую заявку "+prior.ID+" отклонили: "+rejectionReason(prior)+" Обсуди отказ с руководителем или наставником. Для повторной подачи напиши новое рабочее обоснование начиная с «Для …».")
					break
				}
				recommended := false
				if frame.Profile.TeamID != nil {
					for _, team := range r.RecommendedTeams {
						if team == *frame.Profile.TeamID {
							recommended = true
						}
					}
				}
				if !recommended && state.OutsideAccepted != r.ID && work.Message.Kind == "text" {
					state.Stage = "outside_access"
					texts = append(texts, "Ресурс не входит в рекомендации команды. Лучше уточнить у наставника. Напиши «Всё равно оформить» или «Сначала спрошу наставника»; действие пока не выполняется.")
					break
				}
				if params.Level == "" && len(r.Levels) > 1 {
					levels := []string{r.DefaultLevel}
					for _, level := range r.Levels {
						if level != r.DefaultLevel {
							levels = append(levels, level)
						}
					}
					texts = append(texts, "Выбери уровень в форме: "+strings.Join(levels, ", ")+". Уровень по умолчанию указан первым. Напиши «не знаю», чтобы выбрать его с пояснением.")
					results = append(results, map[string]any{"access_form": params})
					break
				}
				if params.Justification == "" && recommended && r.Kind != "task" {
					params.Justification = "Для выполнения шагов демонстрационного плана адаптации"
				}
				state.Current.Slots.Level = params.Level
				state.Current.Slots.Justification = params.Justification
				if params.Level == "" || params.Justification == "" {
					texts = append(texts, "Уточни уровень и рабочую задачу в форме. Для ресурса по задаче нужно своё обоснование; при сомнении обсуди его с наставником.")
					results = append(results, map[string]any{"access_form": params})
					break
				}
				raw, _ := json.Marshal(params)
				clean, reservation, e := b.Tools.Prepare(ctx, frame.Profile, "access_create", raw)
				if e == nil {
					e = b.Store.Conflict(ctx, frame.Profile.ID, reservation)
				}
				if e != nil {
					texts = append(texts, "Не удалось подготовить заявку: "+safeCode(e)+". Проверь существующие заявки и доступы; новая карточка не создана.")
					break
				}
				card, e := b.Store.CreateCardForMessage(ctx, frame.Profile.ID, "access_create", clean, b.Tools.Clock.Now(), b.CardTTL, work.Message.ID)
				if e != nil {
					return "Не удалось сохранить карточку. Ничего не отправлено.", nil, "STORAGE_UNAVAILABLE"
				}
				state.CardID = card.ID
				state.CardVersion = card.Version
				state.Stage = "confirm_card"
				texts = append(texts, "Подготовлена демонстрационная заявка. Проверь параметры и подтверди карточку; до подтверждения ничего не отправлено.")
				results = append(results, map[string]any{"card": card})
			}
			if !found {
				texts = append(texts, "Не нашёл доступного ресурса. Уточни название у наставника.")
			}
		case "time_off_request":

			if intent.Slots.RequestID != "" {
				old, e := b.Tools.Remote.Get(ctx, frame.Profile.ID, "time_off", intent.Slots.RequestID)
				if e != nil {
					texts = append(texts, "Не удалось проверить исходный запрос. Ничего не меняется.")
					continue
				}
				start, end := intent.Slots.StartDate, intent.Slots.EndDate
				if _, fields := calendar.Validate(b.Tools.Clock.Now(), b.Tools.Location, start, end); len(fields) > 0 {
					texts = append(texts, "Уточни допустимые новые даты; старый запрос пока не меняется.")
					results = append(results, map[string]any{"fields": fields})
					continue
				}
				if old.Status == "rejected" || old.Status == "cancelled" {
					texts = append(texts, "Исходный запрос уже закрыт. Можно создать новый отдельный запрос.")
					continue
				}
				firstKind := "time_off_create"
				first := tools.Parameters{StartDate: start, EndDate: end}
				second := agent.Intent{Name: "cancel", Slots: agent.Slots{RequestID: old.ID}, Confidence: 1}
				warning := "Сначала создадим новый запрос. Старый отменим только после результата и отдельного подтверждения."
				if old.StartDate <= end && old.EndDate >= start {
					firstKind = "time_off_cancel"
					first = tools.Parameters{RequestID: old.ID, Version: old.Version}
					second = agent.Intent{Name: "time_off_request", Slots: agent.Slots{StartDate: start, EndDate: end}, Confidence: 1}
					warning = "Даты пересекаются: сначала отменим старый запрос. Создание нового потребует отдельного подтверждения и может не пройти; старый автоматически не восстановится."
				}
				raw, _ := json.Marshal(first)
				clean, reservation, e := b.Tools.Prepare(ctx, frame.Profile, firstKind, raw)
				if e == nil {
					e = b.Store.Conflict(ctx, frame.Profile.ID, reservation)
				}
				if e != nil {
					texts = append(texts, "Перенос не подготовлен: "+safeCode(e))
					continue
				}
				firstRoute, secondRoute, routeErr := b.Store.TransferExecutions(ctx, work, state.ExecutionID)
				if routeErr != nil {
					return "Не удалось сохранить маршрут переноса.", nil, "STORAGE_UNAVAILABLE"
				}
				card, e := b.Store.CreateCardForMessage(execution.WithRoute(ctx, firstRoute), frame.Profile.ID, firstKind, clean, b.Tools.Clock.Now(), b.CardTTL, work.Message.ID)
				if e != nil {
					return "Не удалось сохранить карточку переноса.", nil, "STORAGE_UNAVAILABLE"
				}
				state.Transfer = &TransferPlan{old.ID, start, end, card.ID}
				state.Pending = []agent.Intent{second}
				state.PendingExecutions = []string{secondRoute}
				state.ExecutionID = firstRoute
				state.CardID = card.ID
				state.CardVersion = card.Version
				state.Stage = "confirm_card"
				texts = append(texts, warning+" Проверь и подтверди первое действие.")
				results = append(results, map[string]any{"card": card})
				continue
			}
			if e := authz.ServiceAction(frame.Profile, b.Tools.Clock.Now(), b.Tools.Location); e != nil {
				texts = append(texts, "Сервисные действия доступны только стажёру после даты старта.")
				continue
			}
			params := tools.Parameters{StartDate: intent.Slots.StartDate, EndDate: intent.Slots.EndDate}
			if params.StartDate == "" {
				dateQuery := intent.Slots.Query
				if dateQuery == "" {
					dateQuery = work.Message.Text
				}
				a, z, ok := calendar.Parse(dateQuery, b.Tools.Clock.Now(), b.Tools.Location)
				if ok {
					params.StartDate, params.EndDate = a, z
				}
			}
			state.Current.Slots.StartDate = params.StartDate
			state.Current.Slots.EndDate = params.EndDate
			if params.StartDate == "" || params.EndDate == "" {
				texts = append(texts, "Уточни точные даты через форму отгула. Причину указывать не нужно.")
				results = append(results, map[string]any{"time_off_form": params})
				continue
			}
			if _, fields := calendar.Validate(b.Tools.Clock.Now(), b.Tools.Location, params.StartDate, params.EndDate); len(fields) > 0 {
				texts = append(texts, "Даты пока не подходят. Исправь все указанные нарушения в форме.")
				results = append(results, map[string]any{"fields": fields})
				continue
			}
			prior, e := b.priorRejection(ctx, frame.Profile, "time_off_create", params)
			if e != nil {
				texts = append(texts, "Не удалось проверить предыдущие запросы. Карточка не создана.")
				continue
			}
			if prior != nil && intent.Slots.Query != reapplyQuery {
				state.RejectedID = prior.ID
				state.Stage = "reapply"
				results = append(results, map[string]string{"prompt": "reapply"})
				texts = append(texts, "Прошлый запрос "+prior.ID+" отклонили: "+rejectionReason(prior)+" Обсуди отказ с руководителем. Если всё равно хочешь подать на эти даты, ответь «да».")
				continue
			}
			raw, _ := json.Marshal(params)
			clean, reservation, e := b.Tools.Prepare(ctx, frame.Profile, "time_off_create", raw)
			if e == nil {
				e = b.Store.Conflict(ctx, frame.Profile.ID, reservation)
			}
			if e != nil {
				texts = append(texts, "Не удалось подготовить отгул: "+safeCode(e)+". Ничего не отправлено; при отсутствии руководителя обратись к куратору.")
				continue
			}
			card, e := b.Store.CreateCardForMessage(ctx, frame.Profile.ID, "time_off_create", clean, b.Tools.Clock.Now(), b.CardTTL, work.Message.ID)
			if e != nil {
				return "Не удалось сохранить карточку. Ничего не отправлено.", nil, "STORAGE_UNAVAILABLE"
			}
			state.CardID = card.ID
			state.CardVersion = card.Version
			state.Stage = "confirm_card"
			texts = append(texts, "Проверь даты и подтверди карточку отгула. Данные демонстрационные. После отправки самостоятельно предупреди наставника.")
			results = append(results, map[string]any{"card": card})
		case "card_confirm":
			if state.Stage == "confirm_step" && state.StepID != "" {
				step, e := b.Store.ConfirmStep(ctx, frame.Profile.ID, state.StepID, state.ConfirmationID, b.Tools.Clock.Now(), b.Tools.Location)
				if e != nil {
					texts = append(texts, "Не удалось изменить шаг: "+safeCode(e))
				} else {
					texts = append(texts, "Шаг «"+step.Title+"» изменён после твоего подтверждения.")
					state.Stage = ""
				}
				continue
			}
			cards, e := b.Store.Cards(ctx, frame.Profile.ID, b.Tools.Clock.Now())
			if e != nil {
				return "Не удалось прочитать карточки.", nil, "STORAGE_UNAVAILABLE"
			}
			if state.Stage != "confirm_card" || len(cards) != 1 || cards[0].ID != state.CardID || cards[0].State != "draft" {
				texts = append(texts, "Подтверди конкретную карточку кнопкой под её параметрами. Неоднозначное «да» ничего не отправляет.")
				continue
			}
			card := cards[0]
			attempt, e := b.Store.ConfirmCard(ctx, frame.Profile.ID, card.ID, state.CardVersion, b.Tools.Clock.Now(), b.Tools.Location, func(ctx context.Context, p demo.Profile, c actions.Card) (actions.Reservation, error) {
				_, reservation, e := b.Tools.Prepare(ctx, p, c.Type, c.Parameters)
				return reservation, e
			})
			if e != nil {
				texts = append(texts, "Ничего не отправлено: "+safeCode(e))
				continue
			}
			texts = append(texts, "Подтверждение сохранено. Ожидаем результат сервиса.")
			results = append(results, map[string]any{"related_job": attempt.JobID})
			state.Stage = "waiting_result"
		case "card_cancel", "cancel":

			if intent.Name == "cancel" && intent.Slots.RequestID != "" {
				service := "access"
				kind := "access_cancel"
				if strings.HasPrefix(intent.Slots.RequestID, "ABS-") {
					service = "time_off"
					kind = "time_off_cancel"
				}
				object, e := b.Tools.Remote.Get(ctx, frame.Profile.ID, service, intent.Slots.RequestID)
				if e != nil {
					texts = append(texts, "Не удалось проверить собственный запрос.")
					continue
				}
				raw, _ := json.Marshal(tools.Parameters{RequestID: object.ID, Version: object.Version})
				clean, reservation, e := b.Tools.Prepare(ctx, frame.Profile, kind, raw)
				if e == nil {
					e = b.Store.Conflict(ctx, frame.Profile.ID, reservation)
				}
				if e != nil {
					texts = append(texts, "Отмена не подготовлена: "+safeCode(e))
					continue
				}
				card, e := b.Store.CreateCardForMessage(ctx, frame.Profile.ID, kind, clean, b.Tools.Clock.Now(), b.CardTTL, work.Message.ID)
				if e != nil {
					return "Не удалось сохранить карточку отмены.", nil, "STORAGE_UNAVAILABLE"
				}
				state.CardID = card.ID
				state.CardVersion = card.Version
				state.Stage = "confirm_card"
				texts = append(texts, "Проверь карточку отмены и подтверди отдельное действие.")
				results = append(results, map[string]any{"card": card})
				continue
			}
			if state.Stage == "confirm_step" {
				state.Stage = ""
				state.StepID = ""
				state.ConfirmationID = ""
				texts = append(texts, "Отметка шага не изменена.")
				continue
			}
			if state.Stage == "await_next" {
				state.DeclinedExecutions = state.PendingExecutions
				state.PendingExecutions = nil
				state.Pending = nil
				state.Transfer = nil
				state.Stage = ""
				texts = append(texts, "Второе действие отложено. Уже сохранённые запросы остаются без изменений.")
				continue
			}
			cards, e := b.Store.Cards(ctx, frame.Profile.ID, b.Tools.Clock.Now())
			if e != nil {
				return "Не удалось прочитать карточки.", nil, "STORAGE_UNAVAILABLE"
			}
			if len(cards) == 1 && cards[0].State == "draft" {
				if _, e = b.Store.CancelCard(ctx, frame.Profile.ID, cards[0].ID, cards[0].Version, b.Tools.Clock.Now()); e != nil {
					texts = append(texts, "Не удалось закрыть черновик: "+safeCode(e))
				} else {
					texts = append(texts, "Черновик закрыт. Ничего не отправлено.")
					state.Stage = ""
					state.Current = nil
				}
			} else {
				texts = append(texts, "Выбери карточку для закрытия или собственный запрос для подтверждаемой отмены. Сервисные изменения без подтверждения не выполняются.")
			}
		case "card_edit":
			if state.CardID == "" || state.Current == nil {
				texts = append(texts, "Выбери карточку для изменения.")
				continue
			}
			card, e := b.Store.Card(ctx, frame.Profile.ID, state.CardID, b.Tools.Clock.Now())
			if e != nil || card.State != "draft" {
				texts = append(texts, "Карточка уже изменилась. Открой актуальную версию.")
				continue
			}
			copy := *state.Current
			if intent.Slots.Level != "" {
				copy.Slots.Level = intent.Slots.Level
			}
			if intent.Slots.StartDate != "" {
				copy.Slots.StartDate = intent.Slots.StartDate
			}
			if intent.Slots.EndDate != "" {
				copy.Slots.EndDate = intent.Slots.EndDate
			}
			if intent.Slots.Justification != "" {
				copy.Slots.Justification = intent.Slots.Justification
			}
			query := strings.ToLower(work.Message.Text)
			matches := []string{}
			for _, resource := range frame.Resources {
				matched := tokenMatch(query, resource.ID) || tokenMatch(query, resource.Name)
				for _, alias := range resource.Synonyms {
					if tokenMatch(query, alias) {
						matched = true
					}
				}
				if matched {
					matches = append(matches, resource.ID)
				}
			}
			if len(matches) == 1 {
				copy.Slots.ResourceID = matches[0]
			}
			if strings.Contains(query, "редактирован") {
				copy.Slots.Level = "edit"
			}
			if strings.Contains(query, "чтени") {
				copy.Slots.Level = "read"
			}
			_ = mergeFollowup(&copy, work.Message.Text, b.Tools.Clock.Now(), b.Tools.Location)
			params := tools.Parameters{ResourceID: copy.Slots.ResourceID, Level: copy.Slots.Level, Justification: copy.Slots.Justification, StartDate: copy.Slots.StartDate, EndDate: copy.Slots.EndDate}
			raw, _ := json.Marshal(params)
			clean, _, e := b.Tools.Prepare(ctx, frame.Profile, card.Type, raw)
			if e != nil {
				texts = append(texts, "Параметры не изменены: "+safeCode(e))
				continue
			}
			oldParams, _ := tools.Decode(card.Type, card.Parameters)
			if card.Type == "access_create" && oldParams.ResourceID != params.ResourceID {
				card, e = b.Store.CreateCardForMessage(ctx, frame.Profile.ID, card.Type, clean, b.Tools.Clock.Now(), b.CardTTL, work.Message.ID)
			} else {
				card, e = b.Store.EditCard(ctx, frame.Profile.ID, card.ID, card.Version, clean, b.Tools.Clock.Now(), b.CardTTL)
			}
			if e != nil {
				return "Не удалось сохранить новую версию.", nil, "STORAGE_UNAVAILABLE"
			}
			state.Current = &copy
			state.CardID = card.ID
			state.CardVersion = card.Version
			state.Stage = "confirm_card"
			texts = append(texts, "Параметры изменены. Проверь новую версию карточки и подтверди её.")
			results = append(results, map[string]any{"card": card})
		case "status":
			job, e := b.Store.RefreshJob(ctx, frame.Profile.ID)
			if e != nil {
				return "Не удалось сохранить проверку статусов.", nil, "STORAGE_UNAVAILABLE"
			}
			texts = append(texts, "Проверяю собственные заявки и запросы. Последние сохранённые состояния видны в панели.")
			results = append(results, map[string]any{"related_job": job})
		case "mark_done":
			chosen := intent.Slots.StepID
			if chosen == "" && (strings.Contains(work.Message.Text, "познакомил") || strings.Contains(work.Message.Text, "познакомила")) {
				chosen = frame.Profile.ID + "-P4"
			}
			if chosen == "" {
				texts = append(texts, "Выбери свой ручной шаг в панели. Шаги других владельцев и сервисные шаги менять нельзя.")
				continue
			}
			for _, step := range frame.Steps {
				if step.ID != chosen {
					continue
				}
				c, e := b.Store.StepConfirmation(ctx, frame.Profile, step.ID, step.Version, "done", b.Tools.Clock.Now(), b.StepTTL, b.Tools.Location)
				if e != nil {
					texts = append(texts, "Не удалось подготовить отметку: "+safeCode(e))
					break
				}
				state.Stage = "confirm_step"
				state.StepID = step.ID
				state.ConfirmationID = c.ID
				texts = append(texts, c.Summary+" Ответь «да» или используй кнопку в панели.")
				results = append(results, map[string]string{"prompt": "confirm_step"})
			}

		case "smalltalk":
			texts = append(texts, "Привет! Помогу с планом адаптации, инструкциями, доступом и отгулом. Начнём со следующего шага?")
		default:
			reply := agent.ContactFor(frame, "other", "")
			texts = append(texts, "С этим вопросом я не выполняю действий. "+reply.Text)
			results = append(results, map[string]any{"contact": reply})
		}
	}
	if b.Builder.Current(ctx, frame) != nil {
		return "Не удалось подтвердить актуальные права. Результат скрыт; проверь план заново.", nil, "CONTEXT_UNAVAILABLE"
	}
	if len(state.Pending) > 0 && state.CardID == "" {
		state.Stage = "await_next"
		texts = append(texts, "Есть ещё один запрос. Перейдём к нему? Ответь «да» или «позже».")
	}
	if state.Stage == "card_open" {
		texts = append(texts, "Активная карточка остаётся в истории. Подтвердить её можно отдельной кнопкой.")
	}
	return strings.Join(texts, "\n\n"), map[string]any{"items": results, "mode": provider.Mode()}, ""
}
func safeCode(e error) string {
	messages := map[string]string{"NOT_FOUND": "ресурс не найден или недоступен", "NOT_ALLOWED": "действие недоступно", "BEFORE_START": "стажировка ещё не началась", "VALIDATION_ERROR": "проверь параметры формы", "VERSION_CONFLICT": "карточка изменилась, открой актуальную версию", "CONFLICTING_ATTEMPT": "исход предыдущей попытки ещё уточняется", "ALREADY_GRANTED": "доступ уже выдан", "DUPLICATE_OPEN_REQUEST": "уже есть открытая заявка", "OVERLAPPING_REQUEST": "на эти дни есть активный запрос", "LEVEL_NOT_ALLOWED": "выбранный уровень недоступен", "CANCELLATION_TOO_LATE": "первый день уже наступил, обратись к руководителю"}
	if text, ok := messages[e.Error()]; ok {
		return text
	}
	return "сервис временно недоступен"
}

func executionScenario(intent string) string {
	switch intent {
	case "access_request":
		return "access"
	case "time_off_request", "time_off_info":
		return "time_off"
	default:
		return "orientation"
	}
}
