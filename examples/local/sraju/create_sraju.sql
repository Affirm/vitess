create table user(
  id bigint not null auto_increment,
  -- user_ari varchar(64),
  name varbinary(128),
  primary key(id)
) ENGINE=InnoDB;