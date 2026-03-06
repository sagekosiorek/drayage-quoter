CREATE TABLE email_templates (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  greeting   TEXT NOT NULL DEFAULT 'Hi',
  body       TEXT NOT NULL DEFAULT 'I have a new opportunity. Looking for a partner carrier to handle. Please let me know what your rate would be. Thank you.',
  closing    TEXT NOT NULL DEFAULT 'Kind regards,',
  signature  TEXT NOT NULL DEFAULT '',
  updated_at TEXT,
  UNIQUE(user_id)
);
