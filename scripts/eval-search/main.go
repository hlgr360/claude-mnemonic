// Command eval-search measures search quality against a labelled corpus.
//
//	eval-search seed <dbdir>        writes the corpus into <dbdir>/claude-mnemonic.db
//	eval-search run <port>          queries a running worker and prints MRR@10 and recall@5
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

const (
	project   = "evalproj"
	sessionID = "eval-session"
	dbFile    = "claude-mnemonic.db"
	recallAt  = 5
	mrrAt     = 10
)

//go:embed corpus.json
var corpusJSON []byte

type corpus struct {
	Docs []struct {
		Type      string `json:"type"`
		Title     string `json:"title"`
		Narrative string `json:"narrative"`
	} `json:"docs"`
	Queries []struct {
		Query string `json:"query"`
		Doc   int    `json:"doc"`
	} `json:"queries"`
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: eval-search seed <dbdir> | run <port>")
		os.Exit(2)
	}
	var c corpus
	if err := json.Unmarshal(corpusJSON, &c); err != nil {
		fatal(err)
	}
	var err error
	switch os.Args[1] {
	case "seed":
		err = seed(c, os.Args[2])
	case "run":
		err = run(c, os.Args[2])
	default:
		err = fmt.Errorf("unknown mode %q", os.Args[1])
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// seed stores docs in order, so doc i gets observation id i+1.
func seed(c corpus, dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	store, err := gorm.NewStore(gorm.Config{Path: filepath.Join(dir, dbFile), MaxConns: 1})
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	obsStore := gorm.NewObservationStore(store, nil, nil, nil)
	for i, d := range c.Docs {
		_, _, err := obsStore.StoreObservation(context.Background(), sessionID, project, &models.ParsedObservation{
			Type:      models.ObservationType(d.Type),
			Title:     d.Title,
			Narrative: d.Narrative,
			Concepts:  []string{"eval"},
		}, i+1, 0)
		if err != nil {
			return fmt.Errorf("store doc %d: %w", i, err)
		}
	}
	return nil
}

type obsRef struct {
	ID int64 `json:"id"`
}

func printRanks(query string, want int64, got []obsRef) {
	pos := -1
	for i, o := range got {
		if o.ID == want {
			pos = i + 1
			break
		}
	}
	fmt.Printf("rank=%d results=%d %q\n", pos, len(got), query)
}

func run(c corpus, port string) error {
	client := &http.Client{Timeout: 20 * time.Second}
	var mrr, recall, results float64
	for _, q := range c.Queries {
		u := fmt.Sprintf("http://127.0.0.1:%s/api/context/search?project=%s&query=%s", port, project, url.QueryEscape(q.Query))
		resp, err := client.Get(u)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		var out struct {
			Observations []obsRef `json:"observations"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return fmt.Errorf("decode %q: %w", q.Query, err)
		}
		results += float64(len(out.Observations))
		want := int64(q.Doc + 1)
		if os.Getenv("EVAL_VERBOSE") != "" {
			printRanks(q.Query, want, out.Observations)
		}
		for rank, o := range out.Observations {
			if o.ID != want {
				continue
			}
			if rank < mrrAt {
				mrr += 1 / float64(rank+1)
			}
			if rank < recallAt {
				recall++
			}
			break
		}
	}
	n := float64(len(c.Queries))
	fmt.Printf("queries=%d MRR@%d=%s recall@%d=%s avg_results=%s\n", len(c.Queries), mrrAt, strconv.FormatFloat(mrr/n, 'f', 4, 64),
		recallAt, strconv.FormatFloat(recall/n, 'f', 4, 64), strconv.FormatFloat(results/n, 'f', 2, 64))
	return nil
}
