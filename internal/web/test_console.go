package web

import (
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
)

// handleTest serves the test console, which injects synthetic transmissions
// end-to-end. It is password-protected separately from the rest of the site.
func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	view := testView{Branding: s.cfg.Application.Branding}
	view.IsAuthed = s.testAuthed(r)

	if r.Method == http.MethodPost {
		switch r.FormValue("action") {
		case "auth":
			if secretEqual(r.FormValue("password"), s.cfg.Application.TestPassword) {
				http.SetCookie(w, &http.Cookie{
					Name:     testCookie,
					Value:    s.cfg.Application.TestPassword,
					Path:     "/",
					MaxAge:   testMaxAge,
					HttpOnly: true,
					SameSite: http.SameSiteLaxMode,
				})
				http.Redirect(w, r, "/test", http.StatusFound)
				return
			}
			view.Error = "Incorrect password"

		case "cleanup":
			if view.IsAuthed {
				view.Success, view.Error = s.cleanupTestRecords()
			}

		case "inject":
			if view.IsAuthed {
				view.Success, view.Error = s.injectTestRecord(r)
			}
		}
	}

	if view.IsAuthed {
		view.Units = s.unitOptions()
		if records, err := s.store.TestTranscripts(); err != nil {
			slog.Error("could not count test transcripts", "error", err)
		} else {
			view.TestCount = len(records)
		}
		view.HasTestRecords = view.TestCount > 0
	}

	render(w, http.StatusOK, "test.html", view)
}

func (s *Server) testAuthed(r *http.Request) bool {
	cookie, err := r.Cookie(testCookie)
	if err != nil {
		return false
	}
	return secretEqual(cookie.Value, s.cfg.Application.TestPassword)
}

// cleanupTestRecords deletes every injected transmission and its WAV file.
func (s *Server) cleanupTestRecords() (*testSuccess, string) {
	filenames, err := s.store.DeleteTestTranscripts()
	if err != nil {
		slog.Error("could not delete test transcripts", "error", err)
		return nil, "Failed to delete test transcripts"
	}

	deleted := 0
	for _, filename := range filenames {
		if err := os.Remove(filename); err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("could not delete test recording", "file", filename, "error", err)
			}
			continue
		}
		deleted++
	}
	return &testSuccess{Cleanup: true, Count: len(filenames), FilesDeleted: deleted}, ""
}

// defaultTestUnitID stands in when no unit is chosen or the ID is unparseable.
const defaultTestUnitID = 9999

// injectTestRecord creates a synthetic transmission from the submitted form.
func (s *Server) injectTestRecord(r *http.Request) (*testSuccess, string) {
	transcript := strings.TrimSpace(r.FormValue("transcript"))
	if transcript == "" {
		return nil, "Transcript text is required"
	}

	unitID := defaultTestUnitID
	if raw := strings.TrimSpace(r.FormValue("unit_id")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			unitID = parsed
		}
	}

	unitName := strings.TrimSpace(r.FormValue("custom_unit_name"))
	if unitName == "" {
		unitName = s.cfg.UnitName(unitID)
	}

	result, err := s.processor.InjectFake(transcript, unitID, unitName, s.recordFolder)
	if err != nil {
		slog.Error("could not inject fake message", "error", err)
		return nil, "Failed to create fake message"
	}
	return &testSuccess{Filename: result.Filename, Date: result.Date}, ""
}

// unitOptions lists the configured units for the console's dropdown, ordered
// by radio ID.
func (s *Server) unitOptions() []unitOption {
	options := make([]unitOption, 0, len(s.cfg.Units))
	for id, name := range s.cfg.Units {
		options = append(options, unitOption{ID: id, Name: name})
	}
	sort.Slice(options, func(i, j int) bool { return options[i].ID < options[j].ID })
	return options
}
