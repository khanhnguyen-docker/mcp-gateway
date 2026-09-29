create table catalog_skill (
  id integer primary key,
  catalog_ref text not null,
  uri text not null,
  entry text not null CHECK (json_valid(entry)),
  foreign key (catalog_ref) references catalog(ref) on delete cascade
);

create table skill_added (
  catalog_ref text not null,
  uri text not null,
  manifest_digest text not null,
  added_at datetime default current_timestamp,
  primary key (catalog_ref, uri)
);
