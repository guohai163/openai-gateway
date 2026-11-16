CREATE OR REPLACE FUNCTION notify_request_log_insert() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE
    owner_id text;
BEGIN
    SELECT user_id INTO owner_id FROM gateway_keys WHERE id=NEW.gateway_key_id;
    PERFORM pg_notify('request_logs_' || md5(TG_TABLE_SCHEMA),
        json_build_object('owner_id',owner_id,'kind',NEW.kind)::text);
    RETURN NEW;
END;
$$;

CREATE TRIGGER request_log_inserted AFTER INSERT ON request_logs
FOR EACH ROW EXECUTE FUNCTION notify_request_log_insert();
