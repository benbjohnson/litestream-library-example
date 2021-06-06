package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/benbjohnson/litestream"
	lss3 "github.com/benbjohnson/litestream/s3"
	_ "github.com/mattn/go-sqlite3"
)

// addr is the bind address for the web server.
const addr = ":8080"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	// Parse command line flags.
	dsn := flag.String("dsn", "", "datasource name")
	bucket := flag.String("bucket", "", "s3 replica bucket")
	flag.Parse()
	if *dsn == "" {
		flag.Usage()
		return fmt.Errorf("required: -dsn PATH")
	} else if *bucket == "" {
		flag.Usage()
		return fmt.Errorf("required: -bucket NAME")
	}

	// Create a Litestream DB and attached replica to manage background replication.
	lsdb, err := replicate(ctx, *dsn, *bucket)
	if err != nil {
		return err
	}
	defer lsdb.SoftClose()

	// Open database file.
	db, err := sql.Open("sqlite3", *dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	// Create table for storing page views.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS page_views (id INTEGER PRIMARY KEY, timestamp TEXT);`); err != nil {
		return fmt.Errorf("cannot create table: %w", err)
	}

	// Run web server.
	fmt.Printf("listening on %s\n", addr)
	go http.ListenAndServe(addr,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Start a transaction.
			tx, err := db.Begin()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer tx.Rollback()

			// Store page view.
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO page_views (timestamp) VALUES (?);`, time.Now().Format(time.RFC3339)); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			// Sync litestream with current state.
			if err := lsdb.Sync(r.Context()); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			// Grab current position.
			pos, err := lsdb.Pos()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			// Read total page views.
			var n int
			if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(1) FROM page_views;`).Scan(&n); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			// Commit transaction.
			if err := tx.Commit(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			// Sync litestream with current state again.
			if err := lsdb.Sync(r.Context()); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			// Grab new transaction position.
			newPos, err := lsdb.Pos()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			// Sync litestream with S3.
			startTime := time.Now()
			if err := lsdb.Replicas[0].Sync(r.Context()); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			log.Printf("new transaction: pre=%s post=%s elapsed=%s", pos.String(), newPos.String(), time.Since(startTime))

			// Print total page views.
			fmt.Fprintf(w, "This server has been visited %d times.\n", n)
		}),
	)

	// Wait for signal.
	<-ctx.Done()
	log.Print("myapp received signal, shutting down")

	return nil
}

func replicate(ctx context.Context, dsn, bucket string) (*litestream.DB, error) {
	// Create Litestream DB reference for managing replication.
	lsdb := litestream.NewDB(dsn)

	// Build S3 replica and attach to database.
	client := lss3.NewReplicaClient()
	client.Bucket = bucket

	replica := litestream.NewReplica(lsdb, "s3")
	replica.Client = client

	lsdb.Replicas = append(lsdb.Replicas, replica)

	if err := restore(ctx, replica); err != nil {
		return nil, err
	}

	// Initialize database.
	if err := lsdb.Open(); err != nil {
		return nil, err
	}

	return lsdb, nil
}

func restore(ctx context.Context, replica *litestream.Replica) (err error) {
	// Skip restore if local database already exists.
	if _, err := os.Stat(replica.DB().Path()); err == nil {
		fmt.Println("local database already exists, skipping restore")
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	// Configure restore to write out to DSN path.
	opt := litestream.NewRestoreOptions()
	opt.OutputPath = replica.DB().Path()
	opt.Logger = log.New(os.Stderr, "", log.LstdFlags|log.Lmicroseconds)

	// Determine the latest generation to restore from.
	if opt.Generation, _, err = replica.CalcRestoreTarget(ctx, opt); err != nil {
		return err
	}

	// Only restore if there is a generation available on the replica.
	// Otherwise we'll let the application create a new database.
	if opt.Generation == "" {
		fmt.Println("no generation found, creating new database")
		return nil
	}

	fmt.Printf("restoring replica for generation %s\n", opt.Generation)
	if err := replica.Restore(ctx, opt); err != nil {
		return err
	}
	fmt.Println("restore complete")
	return nil
}
