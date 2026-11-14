WITH counts AS (
 SELECT count(*)::float AS total,count(*) FILTER(WHERE state IN ('succeeded','rejected'))::float AS known,
 count(*) FILTER(WHERE state IN ('unavailable','unknown','manual_review'))::float AS faults FROM attempts
), answers AS (
 SELECT count(*) FILTER(WHERE code='answer_shown' AND scenario IN ('find_doc','time_off_info'))::float AS total,
 count(*) FILTER(WHERE code='answer_shown' AND scenario IN ('find_doc','time_off_info') AND outcome='has_source')::float AS sources,
 count(*) FILTER(WHERE code='answer_shown' AND scenario IN ('find_doc','time_off_info') AND outcome='no_source')::float AS missing,
 count(*) FILTER(WHERE code='intent_classified' AND scenario NOT IN ('smalltalk','out_of_scope','unclear'))::float AS questions,
 count(*) FILTER(WHERE code='answer_shown' AND outcome='has_source')::float AS supported,
 count(*) FILTER(WHERE code='answer_shown' AND outcome IN ('contact','buddy','manager','hr','other')) AS escalations FROM events
) , organizational AS (
 SELECT j.message_id,
 EXISTS (SELECT 1 FROM jsonb_array_elements(COALESCE(j.result->'items','[]')) item WHERE jsonb_array_length(COALESCE(item->'answer'->'sources','[]'))>0) AS sourced,
 EXISTS (SELECT 1 FROM cards c JOIN attempts a ON a.card_id=c.id WHERE c.origin_message_id=j.message_id AND a.state='succeeded') AS saved,
 EXISTS (SELECT 1 FROM jsonb_array_elements(COALESCE(j.result->'items','[]')) item WHERE item->'contact' IS NOT NULL AND item->'contact'<>'null'::jsonb) AS escalated
 FROM jobs j WHERE j.kind='message' AND j.state='done' AND j.payload->>'intent' NOT IN ('smalltalk','out_of_scope','unclear')
), resolution AS (
 SELECT count(*)::float AS questions,count(*) FILTER(WHERE (sourced OR saved) AND NOT escalated)::float AS resolved FROM organizational
), card_metrics AS (
 SELECT count(*) FILTER(WHERE code='card_shown')::float AS shown,count(*) FILTER(WHERE code IN ('card_edited','card_cancelled'))::float AS changed FROM events
), steps AS (
 SELECT count(*)::float AS total,count(*) FILTER(WHERE s.status='done' AND (s.updated_at AT TIME ZONE current_setting('TimeZone'))::date < u.start_date+s.period*7)::float AS timely FROM plan_steps s JOIN users u ON u.id=s.owner_id WHERE u.role='intern' AND s.period<=2
), llm AS (
 SELECT percentile_cont(0.95) WITHIN GROUP(ORDER BY duration_ms) AS p95,
 count(*) FILTER(WHERE outcome='timeout')::float/NULLIF(count(*),0) AS timeouts FROM events WHERE code='llm_call'
), access AS (
 SELECT avg(extract(epoch FROM (o.observed_at-a.started))) AS seconds
 FROM service_objects o JOIN LATERAL (
 SELECT min(COALESCE(m.operational_created_at,t.operational_created_at)) AS started
 FROM attempts t JOIN cards c ON c.id=t.card_id LEFT JOIN messages m ON m.id=c.origin_message_id
 WHERE t.user_id=o.user_id AND t.parameters->>'resource_id'=o.payload->>'resource_id'
 ) a ON true JOIN users u ON u.id=o.user_id
 WHERE o.service='access' AND o.payload->>'status'='approved' AND o.payload->>'resource_id'='space-'||u.team_id AND o.observed_at>=a.started
)
SELECT json_build_object(
 'mode','technical_proxies_only',
 'U-2',json_build_object('resolved_messages',resolution.resolved,'organizational_messages',resolution.questions,'confirmed_actions',(SELECT count(*) FROM attempts WHERE state='succeeded'),'ratio',resolution.resolved/NULLIF(resolution.questions,0)),
 'U-3',answers.escalations,
 'S-2_seconds',access.seconds,
 'S-3',steps.timely/NULLIF(steps.total,0),
 'Q-1',counts.known/NULLIF(counts.total,0),
 'Q-2',answers.sources/NULLIF(answers.total,0),
 'Q-3',answers.missing/NULLIF(answers.total,0),
 'Q-4',card_metrics.changed/NULLIF(card_metrics.shown,0),
 'T-1_p95_ms',(SELECT json_object_agg(scenario,p95) FROM (SELECT scenario,percentile_cont(0.95) WITHIN GROUP(ORDER BY duration_ms) AS p95 FROM events WHERE code='local_request' GROUP BY scenario) t),
 'T-2_p95_ms',llm.p95,'T-2_timeout_ratio',llm.timeouts,
 'T-3',(SELECT count(DISTINCT attempt_id)::float FROM events WHERE code='attempt_outcome' AND outcome IN ('unavailable','unknown','manual_review'))/NULLIF(counts.total,0),
 'attempts',counts.total,'llm_modes',(SELECT COALESCE(json_agg(DISTINCT scenario),'[]') FROM events WHERE code='llm_call')
) FROM counts,answers,card_metrics,steps,llm,access,resolution;
