// store_test_helpers_test.go builds focused PostgreSQL stores with the same shared-state wiring used by the production constructor.
// store_test_helpers_test.go 用于按生产构造器相同的共享状态接线方式创建聚焦测试所需的 PostgreSQL Store。
package vldb_postgres

// newTestStore returns one repository-wired store without opening a database connection, allowing SQL and timeout tests to exercise production ownership boundaries.
// newTestStore 用于返回一个不建立数据库连接但已完成仓储接线的 Store，让 SQL 与超时测试覆盖生产职责边界。
func newTestStore(cfg Config) *Store {
	shared := &storeShared{cfg: cfg, dialect: newSearchDialect(cfg.Flavor)}
	return &Store{shared: shared, repos: newStoreRepositories(shared)}
}
