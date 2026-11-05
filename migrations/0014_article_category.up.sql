-- "Articles": the business blog section, separate from news (category "news") and the community/product posts.
INSERT INTO categories (id, key, names, sort_order) VALUES
 ('0198f000-0000-7000-8000-000000000004','article','{"en":"Articles"}',4)
ON CONFLICT (key) DO NOTHING;
