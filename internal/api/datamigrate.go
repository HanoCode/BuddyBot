package api

import (
	"context"

	"workbuddy-desktop/internal/core"
)

// DataMigrateAPI 账号数据迁移/恢复
// （Session 归并 / 记忆合并 / Connectors 合并，迁移前自动备份、可整体回滚）
type DataMigrateAPI struct{}

// NewDataMigrateAPI 创建数据迁移API
func NewDataMigrateAPI() *DataMigrateAPI {
	return &DataMigrateAPI{}
}

// Diagnose 诊断：当前登录身份 + 各 user_id 的数据分布 + 备份目录。
func (a *DataMigrateAPI) Diagnose(ctx context.Context) (*core.DataMigrationDiag, error) {
	return core.DiagnoseDataMigration()
}

// Migrate 执行迁移：source 账号的数据并入 target（合并而非覆盖；自动备份）。
func (a *DataMigrateAPI) Migrate(ctx context.Context, source, target string) (*core.DataMigrationResult, error) {
	return core.RunDataMigration(source, target)
}

// Backups 列出可用备份标签。
func (a *DataMigrateAPI) Backups(ctx context.Context) ([]string, error) {
	return core.ListDataMigrationBackups()
}

// Rollback 按备份标签整体还原。
func (a *DataMigrateAPI) Rollback(ctx context.Context, tag string) error {
	return core.RollbackDataMigration(tag)
}
