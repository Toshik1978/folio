-- +goose Up
-- +goose StatementBegin
ALTER TABLE books ADD COLUMN cover_state INTEGER NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE books DROP COLUMN cover_state;
-- +goose StatementEnd
