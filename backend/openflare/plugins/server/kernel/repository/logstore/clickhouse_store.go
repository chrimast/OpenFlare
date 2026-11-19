// Copyright 2026 Arctel.net
// SPDX-License-Identifier: Apache-2.0

package logstore

import (
	"context"
	"fmt"
	"math"
	"time"

	"Wavelet/openflare/plugins/server/kernel/model"
	analyticsmodel "Wavelet/openflare/plugins/server/kernel/model/analytics"
	analyticsrepo "Wavelet/openflare/plugins/server/kernel/repository/analytics"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// clickhouseLogStore 实现 AccessLogStore / ObservabilityStore / StatusStore。
// UserAccessLogStore 由 clickhouseUserAccessLogStore 实现（List/Count 方法名已被
// AccessLogStore 占用，Go 不允许同名不同签名方法）。
type clickhouseLogStore struct {
	// skipFreeze 为 true 时跳过迁移冻结检查（仅迁移目标 store 使用）。
	skipFreeze bool
}

func newClickHouseStore() *clickhouseLogStore { return &clickhouseLogStore{} }

// 编译期断言。
var (
	_ AccessLogStore     = (*clickhouseLogStore)(nil)
	_ ObservabilityStore = (*clickhouseLogStore)(nil)
	_ StatusStore        = (*clickhouseLogStore)(nil)
	_ UserAccessLogStore = (*clickhouseUserAccessLogStore)(nil)
)

func chConn(ctx context.Context) (driver.Conn, error) {
	return analyticsrepo.ChConn(ctx)
}

func chConn() (driver.Conn, error) {
	if err := chConnErr(); err != nil {
		return nil, err
	}
	return db.ChConn, nil
}

// ensureWritable 迁移冻结期拒绝写入。
func (s *clickhouseLogStore) ensureWritable(ctx context.Context) error {
	if !s.skipFreeze && Migrating(ctx) {
		return ErrMigrating
	}
	return nil
}

// ListForMigration 按 id 升序分页读取（迁移复制用）：直接查询 CH 原生表。
func (s *clickhouseLogStore) ListForMigration(ctx context.Context, afterID uint64, limit int) ([]analyticsmodel.NodeAccessLog, error) {
	conn, err := chConn(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, `
SELECT `+analyticsmodel.NodeAccessLog{}.InsertColumns()+`
FROM `+analyticsmodel.NodeAccessLog{}.TableName()+`
WHERE id > ?
ORDER BY id ASC
LIMIT ?`, afterID, limitOr(limit, migrationPageSize))
	if err != nil {
		return nil, fmt.Errorf("list node access logs for migration: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []analyticsmodel.NodeAccessLog
	for rows.Next() {
		var item analyticsmodel.NodeAccessLog
		if err := rows.Scan(
			&item.ID,
			&item.NodeID,
			&item.LoggedAt,
			&item.RemoteAddr,
			&item.Region,
			&item.Host,
			&item.Path,
			&item.UserAgent,
			&item.CacheStatus,
			&item.StatusCode,
			&item.BytesSent,
			&item.RequestLength,
			&item.RequestTimeMs,
			&item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan node access log row: %w", err)
		}
		item.LoggedAt = item.LoggedAt.UTC()
		item.CreatedAt = item.CreatedAt.UTC()
		result = append(result, item)
	}
	return result, nil
}

// ---- ObservabilityStore ----

// InsertMetricSnapshot 写入入口：冻结检查 + 经 hook 入队（异步），不直接落库。
func (s *clickhouseLogStore) InsertMetricSnapshot(ctx context.Context, record *model.OpenFlareMetricSnapshot) error {
	if record == nil {
		return nil
	}
	if err := s.ensureWritable(ctx); err != nil {
		return err
	}
	if h := currentObservabilityHooks().QueueMetricSnapshot; h != nil {
		h(toAnalyticsNodeMetricSnapshot(record))
	}
	return nil
}

func (s *clickhouseLogStore) ListMetricSnapshots(ctx context.Context, nodeID string, since time.Time, limit int) ([]*model.OpenFlareMetricSnapshot, error) {
	rows, err := analyticsrepo.ListNodeMetricSnapshots(ctx, toNodeObservabilityFilter(nodeID, since, limit))
	if err != nil {
		return nil, err
	}
	return fromAnalyticsNodeMetricSnapshots(rows), nil
}

func (s *clickhouseLogStore) DeleteAllMetricSnapshots(ctx context.Context) (int64, error) {
	if err := s.ensureWritable(ctx); err != nil {
		return 0, err
	}
	return analyticsrepo.DeleteAllNodeMetricSnapshots(ctx)
}

// ListTrafficHourly 委托 analyticsrepo 读 of_access_log_hourly rollup（M5 口径，UV 恒 0）。
func (s *clickhouseLogStore) ListTrafficHourly(ctx context.Context, nodeID string, since time.Time) ([]analyticsmodel.NodeTrafficHourly, error) {
	return analyticsrepo.ListNodeTrafficHourly(ctx, toNodeObservabilitySince(nodeID, since))
}

// ListAccessLogHourly 委托 analyticsrepo 读 of_access_log_hourly rollup。
func (s *clickhouseLogStore) ListAccessLogHourly(ctx context.Context, nodeID string, since time.Time) ([]analyticsmodel.AccessLogHourly, error) {
	return analyticsrepo.ListAccessLogHourly(ctx, toNodeObservabilitySince(nodeID, since))
}

// ListMetricHourly 委托 analyticsrepo ListNodeMetricHourly：rollup 覆盖窗口时读
// of_node_metric_capacity_hourly，否则按 mergeNodeMetricHourlyPreferRollup 合并 raw 兜底。
func (s *clickhouseLogStore) ListMetricHourly(ctx context.Context, nodeID string, since time.Time) ([]analyticsmodel.NodeMetricHourly, error) {
	return analyticsrepo.ListNodeMetricHourly(ctx, toNodeObservabilitySince(nodeID, since))
}

func (s *clickhouseLogStore) DeleteMetricSnapshotsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if err := s.ensureWritable(ctx); err != nil {
		return 0, err
	}
	return analyticsrepo.DeleteNodeMetricSnapshotsBefore(ctx, cutoff)
}

// BatchInsertNodeMetricSnapshots 是 batchwriter flush 目标：CH 原生批量写入。
func (s *clickhouseLogStore) BatchInsertNodeMetricSnapshots(ctx context.Context, rows []analyticsmodel.NodeMetricSnapshot) error {
	if err := s.ensureWritable(ctx); err != nil {
		return err
	}
	return analyticsrepo.BatchInsertNodeMetricSnapshots(ctx, rows)
}

// InsertEdgeHealth 写入入口：冻结检查 + 经 hook 入队（异步），不直接落库。
func (s *clickhouseLogStore) InsertEdgeHealth(ctx context.Context, record *model.OpenFlareEdgeHealth) error {
	if record == nil {
		return nil
	}
	if err := s.ensureWritable(ctx); err != nil {
		return err
	}
	if h := currentObservabilityHooks().QueueEdgeHealth; h != nil {
		h(toAnalyticsNodeEdgeHealth(record))
	}
	return nil
}

func (s *clickhouseLogStore) ListEdgeHealth(ctx context.Context, nodeID string, since time.Time, limit int) ([]*model.OpenFlareEdgeHealth, error) {
	rows, err := analyticsrepo.ListNodeEdgeHealth(ctx, toNodeObservabilityFilter(nodeID, since, limit))
	if err != nil {
		return nil, err
	}
	return fromAnalyticsNodeEdgeHealths(rows), nil
}

func (s *clickhouseLogStore) DeleteAllEdgeHealth(ctx context.Context) (int64, error) {
	if err := s.ensureWritable(ctx); err != nil {
		return 0, err
	}
	return analyticsrepo.DeleteAllNodeEdgeHealth(ctx)
}

func (s *clickhouseLogStore) DeleteEdgeHealthBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if err := s.ensureWritable(ctx); err != nil {
		return 0, err
	}
	return analyticsrepo.DeleteNodeEdgeHealthBefore(ctx, cutoff)
}

// BatchInsertNodeEdgeHealth 是 batchwriter flush 目标：CH 原生批量写入。
func (s *clickhouseLogStore) BatchInsertNodeEdgeHealth(ctx context.Context, rows []analyticsmodel.NodeEdgeHealth) error {
	if err := s.ensureWritable(ctx); err != nil {
		return err
	}
	return analyticsrepo.BatchInsertNodeEdgeHealth(ctx, rows)
}

// InsertNodeObservationFrps 写入入口：冻结检查 + 经 hook 入队（异步），不直接落库。
func (s *clickhouseLogStore) InsertNodeObservationFrps(ctx context.Context, record *model.OpenFlareNodeObservationFrps) error {
	if record == nil {
		return nil
	}
	if err := s.ensureWritable(ctx); err != nil {
		return err
	}
	if h := currentObservabilityHooks().QueueNodeObsFrps; h != nil {
		h(toAnalyticsNodeObsFrps(record))
	}
	return nil
}

func (s *clickhouseLogStore) ListNodeObservationFrps(ctx context.Context, nodeID string, since time.Time, limit int) ([]*model.OpenFlareNodeObservationFrps, error) {
	rows, err := analyticsrepo.ListNodeObsFrps(ctx, toNodeObservabilityFilter(nodeID, since, limit))
	if err != nil {
		return nil, err
	}
	return fromAnalyticsNodeObsFrps(rows), nil
}

func (s *clickhouseLogStore) DeleteAllNodeObservationFrps(ctx context.Context) (int64, error) {
	if err := s.ensureWritable(ctx); err != nil {
		return 0, err
	}
	return analyticsrepo.DeleteAllNodeObsFrps(ctx)
}

func (s *clickhouseLogStore) DeleteNodeObservationFrpsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if err := s.ensureWritable(ctx); err != nil {
		return 0, err
	}
	return analyticsrepo.DeleteNodeObsFrpsBefore(ctx, cutoff)
}

// BatchInsertNodeObsFrps 是 batchwriter flush 目标：CH 原生批量写入。
func (s *clickhouseLogStore) BatchInsertNodeObsFrps(ctx context.Context, rows []analyticsmodel.NodeObsFrps) error {
	if err := s.ensureWritable(ctx); err != nil {
		return err
	}
	return analyticsrepo.BatchInsertNodeObsFrps(ctx, rows)
}

// InsertNodeObservationFrpc 写入入口：冻结检查 + 经 hook 入队（异步），不直接落库。
func (s *clickhouseLogStore) InsertNodeObservationFrpc(ctx context.Context, record *model.OpenFlareNodeObservationFrpc) error {
	if record == nil {
		return nil
	}
	if err := s.ensureWritable(ctx); err != nil {
		return err
	}
	if h := currentObservabilityHooks().QueueNodeObsFrpc; h != nil {
		h(toAnalyticsNodeObsFrpc(record))
	}
	return nil
}

func (s *clickhouseLogStore) ListNodeObservationFrpc(ctx context.Context, nodeID string, since time.Time, limit int) ([]*model.OpenFlareNodeObservationFrpc, error) {
	rows, err := analyticsrepo.ListNodeObsFrpc(ctx, toNodeObservabilityFilter(nodeID, since, limit))
	if err != nil {
		return nil, err
	}
	return fromAnalyticsNodeObsFrpc(rows), nil
}

func (s *clickhouseLogStore) DeleteAllNodeObservationFrpc(ctx context.Context) (int64, error) {
	if err := s.ensureWritable(ctx); err != nil {
		return 0, err
	}
	return analyticsrepo.DeleteAllNodeObsFrpc(ctx)
}

func (s *clickhouseLogStore) DeleteNodeObservationFrpcBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if err := s.ensureWritable(ctx); err != nil {
		return 0, err
	}
	return analyticsrepo.DeleteNodeObsFrpcBefore(ctx, cutoff)
}

// MigrationRange 返回 of_node_access_logs.logged_at 的最小/最大值（空表返回零值）。
func (s *clickhouseLogStore) MigrationRange(ctx context.Context) (time.Time, time.Time, error) {
	return chMigrationRange(ctx, analyticsmodel.NodeAccessLog{}.TableName(), "logged_at")
}

// EnsurePartitions 是 CH 分支 no-op（CH 无 PG 式分区）。
func (s *clickhouseLogStore) EnsurePartitions(_ context.Context, _, _ time.Time) error {
	return nil
}

// DropEmptyPartitions 是 CH 分支 no-op（CH 分区随数据删除自动消失，无独立分区表）。
func (s *clickhouseLogStore) DropEmptyPartitions(_ context.Context, _ time.Time) error {
	return nil
}

// DropExpiredPartitions 是 CH 分支 no-op（CH 无 PG 式分区，retention 仍走 DeleteBefore）。
func (s *clickhouseLogStore) DropExpiredPartitions(_ context.Context, _ time.Time) error {
	return nil
}

// chMigrationRange 查询 CH 表时间列 MIN/MAX；空表（NULL）返回零值。
func chMigrationRange(ctx context.Context, table, column string) (time.Time, time.Time, error) {
	conn, err := chConn(ctx)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	var minTime, maxTime *time.Time
	if err := conn.QueryRow(ctx,
		"SELECT min("+column+"), max("+column+") FROM "+table,
	).Scan(&minTime, &maxTime); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("query migration range %s: %w", table, err)
	}
	if minTime == nil || maxTime == nil {
		return time.Time{}, time.Time{}, nil
	}
	return minTime.UTC(), maxTime.UTC(), nil
}

// ListMetricSnapshotsForMigration 按 id 升序分页读取（迁移复制用）。
func (s *clickhouseLogStore) ListMetricSnapshotsForMigration(ctx context.Context, afterID uint64, limit int) ([]analyticsmodel.NodeMetricSnapshot, error) {
	return chListForMigration(ctx, afterID, limit,
		analyticsmodel.NodeMetricSnapshot{}.TableName(),
		analyticsmodel.NodeMetricSnapshot{}.InsertColumns(),
		scanNodeMetricSnapshotRows)
}

// chObsRow 迁移读取共用的双字段观测行（字符串状态 + 数值计数）：
// edge_health（status/connections）与 obs_frpc（tunnel_status/connected_relays_count）同形状。
type chObsRow struct {
	ID         uint64
	NodeID     string
	CapturedAt time.Time
	Status     string
	Count      int64
	CreatedAt  time.Time
}

func countToInt32(v int64) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}

func scanChObsRow(rows driver.Rows) ([]chObsRow, error) {
	var result []chObsRow
	for rows.Next() {
		var item chObsRow
		if err := rows.Scan(
			&item.ID,
			&item.NodeID,
			&item.CapturedAt,
			&item.Status,
			&item.Count,
			&item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan observation row: %w", err)
		}
		item.CapturedAt = item.CapturedAt.UTC()
		item.CreatedAt = item.CreatedAt.UTC()
		result = append(result, item)
	}
	return result, nil
}

// ListEdgeHealthForMigration 按 id 升序分页读取（迁移复制用）。
func (s *clickhouseLogStore) ListEdgeHealthForMigration(ctx context.Context, afterID uint64, limit int) ([]analyticsmodel.NodeEdgeHealth, error) {
	rows, err := chListForMigration(ctx, afterID, limit,
		analyticsmodel.NodeEdgeHealth{}.TableName(),
		analyticsmodel.NodeEdgeHealth{}.InsertColumns(),
		scanChObsRow)
	if err != nil {
		return nil, err
	}
	out := make([]analyticsmodel.NodeEdgeHealth, len(rows))
	for i, r := range rows {
		out[i] = analyticsmodel.NodeEdgeHealth{ID: r.ID, NodeID: r.NodeID, CapturedAt: r.CapturedAt, Status: r.Status, Connections: r.Count, CreatedAt: r.CreatedAt}
	}
	return out, nil
}

// ListNodeObsFrpsForMigration 按 id 升序分页读取（迁移复制用）。
func (s *clickhouseLogStore) ListNodeObsFrpsForMigration(ctx context.Context, afterID uint64, limit int) ([]analyticsmodel.NodeObsFrps, error) {
	return chListForMigration(ctx, afterID, limit,
		analyticsmodel.NodeObsFrps{}.TableName(),
		analyticsmodel.NodeObsFrps{}.InsertColumns(),
		scanNodeObsFrpsRows)
}

// ListNodeObsFrpcForMigration 按 id 升序分页读取（迁移复制用）。
func (s *clickhouseLogStore) ListNodeObsFrpcForMigration(ctx context.Context, afterID uint64, limit int) ([]analyticsmodel.NodeObsFrpc, error) {
	rows, err := chListForMigration(ctx, afterID, limit,
		analyticsmodel.NodeObsFrpc{}.TableName(),
		analyticsmodel.NodeObsFrpc{}.InsertColumns(),
		scanChObsRow)
	if err != nil {
		return nil, err
	}
	out := make([]analyticsmodel.NodeObsFrpc, len(rows))
	for i, r := range rows {
		out[i] = analyticsmodel.NodeObsFrpc{ID: r.ID, NodeID: r.NodeID, CapturedAt: r.CapturedAt, TunnelStatus: r.Status, ConnectedRelaysCount: countToInt32(r.Count), CreatedAt: r.CreatedAt}
	}
	return out, nil
}

// chListForMigration 执行按 id 升序分页的 CH 原生表查询，并交给 scanner 扫描。
func chListForMigration[T any](ctx context.Context, afterID uint64, limit int, table, columns string, scanner func(driver.Rows) ([]T, error)) ([]T, error) {
	conn, err := chConn(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, `
SELECT `+columns+`
FROM `+table+`
WHERE id > ?
ORDER BY id ASC
LIMIT ?`, afterID, limitOr(limit, migrationPageSize))
	if err != nil {
		return nil, fmt.Errorf("list %s for migration: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	return scanner(rows)
}

// ---- StatusStore ----

// ActiveDatabase 返回当前日志主库名（CH 分支固定 clickhouse）。
func (s *clickhouseLogStore) ActiveDatabase(_ context.Context) (string, error) {
	return dbNameClickHouse, nil
}

// ---- UserAccessLogStore ----

// clickhouseUserAccessLogStore 实现 UserAccessLogStore。clickhouseLogStore 已占用
// List/Count 方法名（AccessLogStore 接口），Go 不允许同名不同签名方法，故用户访问日志
// 用独立类型嵌入同一 clickhouseLogStore（与 userAccessLogGormStore 同构），复用 ensureWritable。
type clickhouseUserAccessLogStore struct {
	*clickhouseLogStore
}

func newClickHouseUserAccessLogStore() *clickhouseUserAccessLogStore {
	return &clickhouseUserAccessLogStore{clickhouseLogStore: newClickHouseStore()}
}

// ListForMigration 按 id 升序分页读取（迁移复制用）。
func (s *clickhouseUserAccessLogStore) ListForMigration(ctx context.Context, afterID uint64, limit int) ([]analyticsmodel.UserAccessLog, error) {
	return chListForMigration(ctx, afterID, limit,
		analyticsmodel.UserAccessLog{}.TableName(),
		analyticsmodel.UserAccessLog{}.InsertColumns(),
		func(rows driver.Rows) ([]analyticsmodel.UserAccessLog, error) {
			var result []analyticsmodel.UserAccessLog
			for rows.Next() {
				var item analyticsmodel.UserAccessLog
				if err := rows.Scan(
					&item.ID,
					&item.UserID,
					&item.Path,
					&item.Method,
					&item.IP,
					&item.UserAgent,
					&item.Headers,
					&item.Status,
					&item.Latency,
					&item.CreatedAt,
				); err != nil {
					return nil, fmt.Errorf("scan user access log row: %w", err)
				}
				item.CreatedAt = item.CreatedAt.UTC()
				result = append(result, item)
			}
			return result, nil
		})
}

// MigrationRange 返回 w_user_access_logs.created_at 的最小/最大值（空表返回零值）。
func (s *clickhouseUserAccessLogStore) MigrationRange(ctx context.Context) (time.Time, time.Time, error) {
	return chMigrationRange(ctx, analyticsmodel.UserAccessLog{}.TableName(), "created_at")
}
