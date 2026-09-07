-- Synthetic Notion Desktop cache for scoped ingestion and CLI smoke checks.
create table space (id text primary key, name text, pages text, settings text, created_time integer, last_edited_time integer);
create table notion_user (id text primary key, name text, email text, given_name text, family_name text, profile_photo text);
create table team (id text primary key, space_id text, parent_id text, parent_table text, name text, description text, team_pages text, settings text, archived_at integer);
create table collection (id text primary key, space_id text, parent_id text, parent_table text, name text, schema text, format text, alive integer);
create table block (id text primary key, space_id text, type text, properties text, content text, collection_id text, created_time integer, last_edited_time integer, parent_id text, parent_table text, alive integer, format text);
create table comment (id text primary key, parent_id text, space_id text, text text, content text, created_by_id text, created_time integer, last_edited_time integer, alive integer);

insert into space values
('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'Selected', '[]', '{}', 1, 1),
('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'Excluded', '[]', '{}', 1, 1);
insert into notion_user values ('shared-user', 'Shared user', '', '', '', '');
insert into team values
('team-a', 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', '', '', 'Selected team', '', '[]', '{}', 0),
('team-b', 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', '', '', 'Excluded team', '', '[]', '{}', 0);
insert into collection values
('collection-a', 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', '', '', 'Selected database', '{}', '{}', 1),
('collection-b', 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', '', '', 'Excluded database', '{}', '{}', 1);
insert into block values
('page-a', 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'page', '{"title":[["Selected page"]]}', '["block-a"]', '', 1, 1, '', '', 1, '{}'),
('block-a', 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'text', '{"title":[["Selected body"]]}', '[]', '', 1, 1, 'page-a', 'block', 1, '{}'),
('page-b', 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'page', '{"title":[["Excluded page"]]}', '["block-b"]', '', 1, 1, '', '', 1, '{}'),
('block-b', 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'text', '{"title":[["Excluded body"]]}', '[]', '', 1, 1, 'page-b', 'block', 1, '{}'),
('page-unknown', null, 'page', '{"title":[["Unknown workspace"]]}', '[]', '', 1, 1, '', '', 1, '{}');
insert into comment values
('comment-a', 'page-a', 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', '[["Selected comment"]]', '[]', 'shared-user', 1, 1, 1),
('comment-b', 'page-b', 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', '[["Excluded comment"]]', '[]', 'shared-user', 1, 1, 1),
('comment-unknown', 'page-unknown', null, '[["Unknown comment"]]', '[]', 'shared-user', 1, 1, 1);
