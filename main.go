// Command portal serves 42.bombatomica64.dev: an index of my 42 projects (status and
// marks from the intra, the rest from projects.yaml) and an info page per project.
//
// /<project> is the app itself: the portal reverse-proxies it to APP_HOST:<port> (the
// port comes from projects.yaml) and shows the info page instead when the app is down.
package main

import (
	"context"
	"embed"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed projects.yaml
var embeddedProjects []byte

//go:embed web/templates/*.html
var templateFS embed.FS

func main() {
	var intra *Intra
	if uid, secret := os.Getenv("FT_UID"), os.Getenv("FT_SECRET"); uid != "" && secret != "" {
		intra = NewIntra(uid, secret)
	} else {
		log.Print("FT_UID/FT_SECRET unset: serving projects.yaml without intra data")
	}

	catalog, err := NewCatalog(intra, os.Getenv("PROJECTS_URL"), os.Getenv("APP_HOST"), embeddedProjects)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go catalog.Run(ctx)

	pages := map[string]*template.Template{}
	for _, name := range []string{"index", "project", "notfound"} {
		pages[name] = template.Must(template.New("").Funcs(funcs).ParseFS(templateFS, "web/templates/layout.html", "web/templates/"+name+".html"))
	}
	render := func(w http.ResponseWriter, status int, name string, data any) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		if err := pages[name].ExecuteTemplate(w, "layout", data); err != nil {
			log.Printf("render %s: %v", name, err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		render(w, http.StatusOK, "index", catalog.Projects())
	})
	info := func(w http.ResponseWriter, status int, key string, offline bool) {
		p, ok := catalog.Project(key)
		if !ok {
			render(w, http.StatusNotFound, "notfound", key)
			return
		}
		if offline {
			p.Live = false
		}
		render(w, status, "project", struct {
			Project
			Offline bool
		}{p, offline})
	}
	proxy := newAppProxy(os.Getenv("APP_HOST"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "300")
		info(w, http.StatusServiceUnavailable, r.PathValue("project"), true)
	})

	mux.HandleFunc("GET /{project}/about", func(w http.ResponseWriter, r *http.Request) {
		info(w, http.StatusOK, r.PathValue("project"), false)
	})
	// Everything else under /<key> is the app itself, any method, websockets included.
	// Projects without an app go to their info page.
	app := func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("project")
		port := catalog.Port(key)
		if port == 0 || proxy == nil {
			http.Redirect(w, r, "/"+key+"/about", http.StatusFound)
			return
		}
		proxy.ServeHTTP(w, withPort(r, port))
	}
	mux.HandleFunc("/{project}", app)
	mux.HandleFunc("/{project}/{rest...}", app)

	addr := ":" + cmpOr(os.Getenv("PORT"), "8080")
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Printf("listening on %s", addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

var funcs = template.FuncMap{
	"ptr": func(t time.Time) *time.Time {
		if t.IsZero() {
			return nil
		}
		return &t
	},
	"date": func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Format("Jan 2006")
	},
}
