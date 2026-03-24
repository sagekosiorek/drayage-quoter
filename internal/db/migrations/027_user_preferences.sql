CREATE TABLE user_preferences (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  default_markup_method TEXT NOT NULL DEFAULT 'flat',
  UNIQUE(user_id)
);
