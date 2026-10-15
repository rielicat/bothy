-- 0010_knowledge_stemming.sql: knowledge search matches other forms of a
-- word, so "redirects" finds code that says redirect. FTS5 cannot change a
-- table's tokenizer, so knowledge_fts is created again with the porter
-- stemmer and rebuilt from knowledge_chunks, its external content. The
-- knowledge_chunks triggers name the table and keep feeding it unchanged.

DROP TABLE knowledge_fts;
CREATE VIRTUAL TABLE knowledge_fts USING fts5(
  path, title, body, terms, content='knowledge_chunks', content_rowid='id',
  tokenize='porter unicode61 remove_diacritics 2'
);
INSERT INTO knowledge_fts(knowledge_fts) VALUES ('rebuild');
