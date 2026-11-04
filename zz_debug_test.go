package rook_test

import (
	"context"
	"encoding/json"

	"github.com/zummed/corbie/client"
)

func (w *world) dumpFailed() {
	runs, _ := w.List(context.Background(), client.Filter{Finished: true})
	for _, r := range runs {
		if r.Machine == "attachments" {
			s := w.show(r.ID)
			b, _ := json.Marshal(s)
			w.t.Logf("RUN %s", b)
			return
		}
	}
}
