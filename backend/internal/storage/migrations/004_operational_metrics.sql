-- Старым строкам не приписываем неизвестное операционное время.
ALTER TABLE messages ADD COLUMN operational_created_at timestamptz;
ALTER TABLE service_objects ADD COLUMN observed_at timestamptz;
