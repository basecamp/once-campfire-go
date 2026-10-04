package database

/*
int campfire_register_wal_hook(void);
*/
import "C"

import (
	"database/sql"
	"errors"
	"log"
	"sync"
	"sync/atomic"
)

// WAL checkpoints run beside the writer, as in reference/crates/db/src/database.rs.
// SQLite's auto-checkpoint makes the commit that leaves the WAL at 1,000 pages or more
// checkpoint (PASSIVE, with fsyncs) before returning, and every write queued behind it
// waits. Instead the writer's commits only report the WAL's size; each 1,000 pages it
// grows wake a checkpointer with its own connection, which runs the same PASSIVE
// checkpoint while writes carry on. Durability is unchanged (WAL, synchronous=NORMAL).
//
// A background checkpoint never catches up with constant writes, so SQLite never
// restarts the WAL. At walLimitPages (~40 MB) the writer restarts it itself (RESTART,
// after any running checkpoint), stalling writes once per 10,000 pages.
const (
	autocheckpointPages = 1000
	walLimitPages       = 10_000
)

type checkpointer struct {
	path string
	conn *sql.DB
	wake chan struct{}
	done chan struct{}
	// Held while a checkpoint runs, so the writer's RESTART waits for it rather than
	// being refused (SQLite runs one checkpoint at a time).
	running sync.Mutex
	// The WAL's size in pages when the checkpointer was last woken; only the single
	// writer's commits use it.
	wokenAt int
	ran     atomic.Int64 // completed background checkpoints
}

var checkpointers sync.Map // database path -> *checkpointer

func registerWALHook() error {
	if C.campfire_register_wal_hook() != 0 {
		return errors.New("could not register the SQLite WAL hook")
	}
	return nil
}

func startCheckpointer(path string, conn *sql.DB) *checkpointer {
	conn.SetMaxOpenConns(1)
	c := &checkpointer{path: path, conn: conn, wake: make(chan struct{}, 1), done: make(chan struct{})}
	checkpointers.Store(path, c)
	go func() {
		defer close(c.done)
		for range c.wake {
			c.running.Lock()
			c.checkpoint("PASSIVE")
			c.running.Unlock()
			c.ran.Add(1)
		}
	}()
	return c
}

func (c *checkpointer) checkpoint(mode string) {
	var busy, frames, copied int
	if err := c.conn.QueryRow("PRAGMA wal_checkpoint("+mode+")").Scan(&busy, &frames, &copied); err != nil {
		log.Printf("WAL checkpoint %s failed: %v", mode, err)
	} else if busy != 0 {
		log.Printf("WAL checkpoint %s couldn't finish", mode)
	}
}

func (c *checkpointer) close() error {
	checkpointers.CompareAndDelete(c.path, c)
	close(c.wake)
	<-c.done
	return c.conn.Close()
}

// What the writer's WAL hook does after a commit.
const (
	walContinue = 0
	walRestart  = 1
	walPassive  = 2
)

//export campfireWALCommitted
func campfireWALCommitted(path *C.char, pages C.int) C.int {
	value, ok := checkpointers.Load(C.GoString(path))
	if !ok {
		// A writer without a checkpointer (closing) keeps SQLite's default behavior.
		if pages >= autocheckpointPages {
			return walPassive
		}
		return walContinue
	}
	c := value.(*checkpointer)
	n := int(pages)
	if n >= walLimitPages {
		c.running.Lock() // released by campfireWALRestarted
		return walRestart
	}
	if n < c.wokenAt {
		c.wokenAt = 0 // the WAL restarted
	}
	if n-c.wokenAt >= autocheckpointPages {
		// While a checkpoint is still due, the next commit tries again.
		select {
		case c.wake <- struct{}{}:
			c.wokenAt = n
		default:
		}
	}
	return walContinue
}

//export campfireWALRestarted
func campfireWALRestarted(path *C.char) {
	if value, ok := checkpointers.Load(C.GoString(path)); ok {
		c := value.(*checkpointer)
		c.wokenAt = 0
		c.running.Unlock()
	}
}
