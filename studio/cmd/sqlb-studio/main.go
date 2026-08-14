// Command sqlb-studio serves a read-only, generic browser over a sqlb.json
// manifest. It carries no per-application knowledge — see the studio package
// doc and docs/adr/0053 in the parent module.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/jryannel/sqlb/studio"
)

func main() {
	manifestPath := flag.String("manifest", "sqlb.json", "path to a sqlb.json manifest")
	addr := flag.String("addr", ":4000", "address to listen on")
	flag.Parse()

	m, err := studio.LoadManifest(*manifestPath)
	if err != nil {
		log.Fatal(err)
	}

	srv, err := studio.NewServer(m)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("sqlb studio: %d table(s) from %s, listening on http://localhost%s\n", len(m.Tables), *manifestPath, *addr)
	log.Fatal(http.ListenAndServe(*addr, srv.Handler()))
}
