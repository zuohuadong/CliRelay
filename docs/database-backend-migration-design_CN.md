# 数据库后端与三库迁移设计文档

状态：已废弃

这份 2026-05 的草案把 SQLite 写成默认库，并规划 SQLite / PostgreSQL / MySQL 三选一。当前运行时已经固定为 PostgreSQL 19+ 和 Ent ORM；Redis 不再是 CliRelay 的运行时状态存储，也不再提供 SQLite 运行时或库间迁移向导。

现行说明见 [`postgres-runtime-migration.md`](postgres-runtime-migration.md)。
