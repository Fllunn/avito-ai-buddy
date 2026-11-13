package agent

import "ai-buddy/backend/platform/demo"

type ContactReply struct {
	Text      string        `json:"text"`
	Contact   *demo.Contact `json:"contact,omitempty"`
	StepState string        `json:"step_state,omitempty"`
}

func ContactFor(frame Context, topic, resourceID string) ContactReply {
	reply := ContactReply{Text: "Уточни вопрос у куратора стажировки. Контакты демонстрационные."}
	id := frame.Profile.CuratorID
	switch topic {
	case "chats", "buddy":
		id = frame.Profile.BuddyID
		reply.Text = "По чатам и рабочим вопросам обратись к наставнику. Контакты демонстрационные."
	case "goals":
		id = frame.Profile.ManagerID
		reply.Text = "Цели и ожидания обсуди с руководителем. Содержание целей помощник не хранит и не определяет. Контакты демонстрационные."
		for _, step := range frame.Steps {
			if step.Code == "P3" && step.InternID == frame.Profile.ID {
				reply.StepState = step.Status
				break
			}
		}
	case "resource":
		found := false
		for _, resource := range frame.Resources {
			if resource.ID == resourceID {
				found = true
				if resource.Kind == "closed" {
					id = frame.Profile.BuddyID
					reply.Text = "Этот ресурс виден, но запросить доступ нельзя. Уточни у наставника. Контакты демонстрационные."
				} else {
					owner := resource.ApproverID
					if owner == "manager" {
						id = frame.Profile.ManagerID
					} else {
						id = &owner
					}
					reply.Text = "По доступу обратись к согласующему ресурса. Контакты демонстрационные."
				}
				break
			}
		}
		if !found {
			reply.Text = "Не нашёл доступного ресурса. Уточни название у наставника."
			id = frame.Profile.BuddyID
		}
	}
	if id == nil {
		id = frame.Profile.CuratorID
	}
	if id != nil {
		for _, contact := range demo.Contacts() {
			if contact.ID == *id {
				reply.Contact = &contact
				break
			}
		}
	}
	return reply
}
