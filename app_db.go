package main

import (
	"database/sql"
	"fmt"

	"gcp-mailnode/internal/store"
)

func requireDB() (*sql.DB, error) {
	db := store.DB()
	if db == nil {
		return nil, fmt.Errorf("数据库未就绪")
	}
	return db, nil
}
