ALTER TABLE execution_links DROP CONSTRAINT execution_links_kind_check;
ALTER TABLE execution_links ADD CONSTRAINT execution_links_kind_check CHECK(kind IN ('message','job','card','attempt','parent','object','operation_parent'));
CREATE INDEX execution_operation_parent ON execution_links(object_id) WHERE kind='operation_parent';
