package controller

import "context"

func (s *Store) ReapExpired(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT node_id FROM nodes ORDER BY node_id`)
	if err != nil {
		return err
	}
	nodes := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		nodes = append(nodes, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, node := range nodes {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if err = lockNode(ctx, tx, node); err == nil {
			err = expireJobs(ctx, tx, node)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
