-- +goose Up
-- updated_at is set by its column default at insert and by nothing
--   afterwards. A trigger is the only place this can live where every writer
--   is covered — including the ones that are not this application.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION touch_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER spaces_touch_updated_at BEFORE UPDATE ON "spaces"
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER notes_touch_updated_at BEFORE UPDATE ON "notes"
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- +goose Down
DROP TRIGGER IF EXISTS notes_touch_updated_at ON "notes";
DROP TRIGGER IF EXISTS spaces_touch_updated_at ON "spaces";
DROP FUNCTION IF EXISTS touch_updated_at();
