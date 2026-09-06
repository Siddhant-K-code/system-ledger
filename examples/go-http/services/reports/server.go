package reports

import (
	"database/sql"
	"net/http"
)

type Server struct {
	db *sql.DB
}

func NewServer(db *sql.DB) *Server {
	return &Server{db: db}
}

func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /reports", s.ListReports)
	mux.HandleFunc("GET /reports/search", s.SearchReports)
	return mux
}
