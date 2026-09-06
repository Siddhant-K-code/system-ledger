package reports

import (
	"encoding/json"
	"net/http"
)

func (s *Server) ListReports(w http.ResponseWriter, r *http.Request) {
	reports, err := s.loadReports(r.Context())
	if err != nil {
		http.Error(w, "reports unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(reports); err != nil {
		return
	}
}

func (s *Server) SearchReports(w http.ResponseWriter, r *http.Request) {
	reports, err := s.searchReports(r.Context(), r.URL.Query().Get("status") != "")
	if err != nil {
		http.Error(w, "search unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(reports); err != nil {
		return
	}
}
