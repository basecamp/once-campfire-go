package web

func (s *Server) registerPWARoutes() {
	s.mux.HandleFunc("GET /webmanifest", s.browserCheck(s.pwaManifest))
	s.mux.HandleFunc("GET /service-worker", s.browserCheck(s.pwaServiceWorker))
}
