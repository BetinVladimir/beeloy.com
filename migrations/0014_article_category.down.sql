DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM articles a JOIN categories c ON c.id = a.category_id WHERE c.key = 'article') THEN
        RAISE EXCEPTION '0014 down: articles in the "article" category exist; move or delete them first';
    END IF;
END $$;
DELETE FROM categories WHERE key = 'article';
