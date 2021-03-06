package database

import (
	"database/sql"
	"github.com/wieku/danser-go/utils"
	"log"
	"os"
	"path/filepath"
	"strings"
	"github.com/wieku/danser-go/settings"

	"github.com/wieku/danser-go/beatmap"
	"crypto/md5"
	"io"
	"encoding/hex"
	_ "github.com/mattn/go-sqlite3"
	"time"
	"strconv"
)

var dbFile *sql.DB

var currentPreVersion = 20181111
const databaseVersion = 20181111

func Init() {
	var err error
	dbFile, err = sql.Open("sqlite3", "danser.db")

	if err != nil {
		panic(err)
	}

	_, err = dbFile.Exec(`CREATE TABLE IF NOT EXISTS beatmaps (dir TEXT, file TEXT, lastModified INTEGER, title TEXT, titleUnicode TEXT, artist TEXT, artistUnicode TEXT, creator TEXT, version TEXT, source TEXT, tags TEXT, cs REAL, ar REAL, sliderMultiplier REAL, sliderTickRate REAL, audioFile TEXT, previewTime INTEGER, sampleSet INTEGER, stackLeniency REAL, mode INTEGER, bg TEXT, pauses TEXT, timingPoints TEXT, md5 TEXT, dateAdded INTEGER, playCount INTEGER, lastPlayed INTEGER, hpdrain REAL, od REAL);
							CREATE INDEX IF NOT EXISTS idx ON beatmaps (dir, file);
							CREATE TABLE IF NOT EXISTS info (key TEXT NOT NULL UNIQUE, value TEXT);`)

	if err != nil {
		panic(err)
	}

	res, _ := dbFile.Query("SELECT key, value FROM info")

	for res.Next() {
		var key string
		var value string

		res.Scan(&key, &value)
		if key == "version" {
			parsed, _ := strconv.ParseInt(value, 10, 32)
			currentPreVersion = int(parsed)
		}
	}

	log.Println("Database version: ", currentPreVersion)

	if currentPreVersion < databaseVersion {
		log.Println("Database is too old! Updating...")
	}

	if currentPreVersion < 20181111 {
		_, err = dbFile.Exec(`ALTER TABLE beatmaps ADD COLUMN hpdrain REAL;
							 ALTER TABLE beatmaps ADD COLUMN od REAL;`)

		if err != nil {
			panic(err)
		}
	}

	_, err = dbFile.Exec("REPLACE INTO info (key, value) VALUES ('version', ?)", strconv.FormatInt(databaseVersion, 10))
	if err != nil {
		log.Println(err)
	}
}

func LoadBeatmaps() []*beatmap.BeatMap {
	log.Println("Checking database...")

	searchDir := settings.General.OsuSongsDir

	_, err := os.Open(searchDir)
	if os.IsNotExist(err) {
		log.Println(searchDir + " does not exist!")
		return nil
	}

	mod := getLastModified()

	newBeatmaps := make([]*beatmap.BeatMap, 0)
	cachedBeatmaps := make([]*beatmap.BeatMap, 0)

	filepath.Walk(searchDir, func(path string, f os.FileInfo, err error) error {
		if strings.HasSuffix(f.Name(), ".osz") {
			log.Println("Unpacking", path, "to", filepath.Dir(path)+"/"+strings.TrimSuffix(f.Name(), ".osz"))
			utils.Unzip(path, filepath.Dir(path)+"/"+strings.TrimSuffix(f.Name(), ".osz"))
			os.Remove(path)
		}
		return nil
	})

	filepath.Walk(searchDir, func(path string, f os.FileInfo, err error) error {
		if strings.HasSuffix(f.Name(), ".osu") {
			cachedTime := mod[filepath.Base(filepath.Dir(path))+"/"+f.Name()]
			if cachedTime != f.ModTime().UnixNano()/1000000 {
				removeBeatmap(filepath.Base(filepath.Dir(path)), f.Name())

				file, err := os.Open(path)

				if err == nil {
					defer file.Close()

					if bMap := beatmap.ParseBeatMapFile(file); bMap != nil {
						bMap.LastModified = f.ModTime().UnixNano() / 1000000
						bMap.TimeAdded = time.Now().UnixNano() / 1000000
						log.Println("Importing:", bMap.File)

						hash := md5.New()
						if _, err := io.Copy(hash, file); err == nil {
							bMap.MD5 = hex.EncodeToString(hash.Sum(nil))
							newBeatmaps = append(newBeatmaps, bMap)
						}
					}
				}
			} else {
				bMap := beatmap.NewBeatMap()
				bMap.Dir = filepath.Base(filepath.Dir(path))
				bMap.File = f.Name()
				cachedBeatmaps = append(cachedBeatmaps, bMap)
			}
		}
		return nil
	})

	log.Println("Imported", len(newBeatmaps), "new beatmaps.")

	updateBeatmaps(newBeatmaps)

	log.Println("Found", len(cachedBeatmaps), "cached beatmaps. Loading...")

	loadBeatmaps(cachedBeatmaps)

	allMaps := append(newBeatmaps, cachedBeatmaps...)

	log.Println("Loaded", len(allMaps), "total.")

	return allMaps
}

func UpdatePlayStats(beatmap *beatmap.BeatMap) {
	_, err := dbFile.Exec("UPDATE beatmaps SET playCount = ?, lastPlayed = ? WHERE dir = ? AND file = ?", beatmap.PlayCount, beatmap.LastPlayed, beatmap.Dir, beatmap.File)
	if err != nil {
		log.Println(err)
	}
}

func removeBeatmap(dir, file string) {
	dbFile.Exec("DELETE FROM beatmaps WHERE dir = ? AND file = ?", dir, file)
}

func loadBeatmaps(bMaps []*beatmap.BeatMap) {

	beatmaps := make(map[string]int)

	for i, bMap := range bMaps {
		beatmaps[bMap.Dir+"/"+bMap.File] = i + 1
	}

	if currentPreVersion < databaseVersion {
		log.Println("Updating cached beatmaps")
		tx, err := dbFile.Begin()
		if err != nil {
			panic(err)
		}

		if currentPreVersion < 20181111 {
			var st *sql.Stmt
			st, err = tx.Prepare("UPDATE beatmaps SET hpdrain = ?, od = ? WHERE dir = ? AND file = ?")
			if err != nil {
				panic(err)
			}

			for _, bMap := range bMaps {
				err2 := beatmap.ParseBeatMap(bMap)
				if err2 != nil {
					log.Println("Corrupted cached beatmap found. Removing from database:", bMap.File)
					removeBeatmap(bMap.Dir, bMap.File)
				} else {
					_, err1 := st.Exec(
						bMap.HPDrainRate,
						bMap.OverallDifficulty,
						bMap.Dir,
						bMap.File)

					if err1 != nil {
						log.Println(err1)
					}
				}
			}

			err = st.Close()
			if err != nil {
				panic(err)
			}
		}

		err = tx.Commit()
		if err != nil {
			panic(err)
		}
	} else {
		res, _ := dbFile.Query("SELECT * FROM beatmaps")

		for res.Next() {
			beatmap := beatmap.NewBeatMap()
			var mode int
			res.Scan(
				&beatmap.Dir,
				&beatmap.File,
				&beatmap.LastModified,
				&beatmap.Name,
				&beatmap.NameUnicode,
				&beatmap.Artist,
				&beatmap.ArtistUnicode,
				&beatmap.Creator,
				&beatmap.Difficulty,
				&beatmap.Source,
				&beatmap.Tags,
				&beatmap.CircleSize,
				&beatmap.AR,
				&beatmap.Timings.SliderMult,
				&beatmap.Timings.TickRate,
				&beatmap.Audio,
				&beatmap.PreviewTime,
				&beatmap.Timings.BaseSet,
				&beatmap.StackLeniency,
				&mode,
				&beatmap.Bg,
				&beatmap.PausesText,
				&beatmap.TimingPoints,
				&beatmap.MD5,
				&beatmap.TimeAdded,
				&beatmap.PlayCount,
				&beatmap.LastPlayed,
				&beatmap.HPDrainRate,
				&beatmap.OverallDifficulty)

			if beatmap.Name+beatmap.Artist+beatmap.Creator == "" || beatmap.TimingPoints == "" {
				log.Println("Corrupted cached beatmap found. Removing from database:", beatmap.File)
				removeBeatmap(beatmap.Dir, beatmap.File)
				continue
			}

			key := beatmap.Dir + "/" + beatmap.File

			if beatmaps[key] > 0 {
				bMaps[beatmaps[key]-1] = beatmap
				beatmap.LoadPauses()
				beatmap.LoadTimingPoints()
			}

		}
	}
}

func updateBeatmaps(bMaps []*beatmap.BeatMap) {
	tx, err := dbFile.Begin()

	if err == nil {
		var st *sql.Stmt
		st, err = tx.Prepare("INSERT INTO beatmaps VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")

		if err == nil {
			for _, bMap := range bMaps {
				_, err1 := st.Exec(bMap.Dir,
					bMap.File,
					bMap.LastModified,
					bMap.Name,
					bMap.NameUnicode,
					bMap.Artist,
					bMap.ArtistUnicode,
					bMap.Creator,
					bMap.Difficulty,
					bMap.Source,
					bMap.Tags,
					bMap.CircleSize,
					bMap.AR,
					bMap.SliderMultiplier,
					bMap.Timings.TickRate,
					bMap.Audio,
					bMap.PreviewTime,
					bMap.Timings.BaseSet,
					bMap.StackLeniency,
					0,
					bMap.Bg,
					bMap.PausesText,
					bMap.TimingPoints,
					bMap.MD5,
					bMap.TimeAdded,
					bMap.PlayCount,
					bMap.LastPlayed,
					bMap.HPDrainRate,
					bMap.OverallDifficulty)

				if err1 != nil {
					log.Println(err1)
				}
			}
		}

		st.Close()
		tx.Commit()
	}

	if err != nil {
		log.Println(err)
	}

}

func getLastModified() map[string]int64 {
	res, _ := dbFile.Query("SELECT dir, file, lastModified FROM beatmaps")

	mod := make(map[string]int64)

	for res.Next() {
		var dir string
		var file string
		var lastModified int64

		res.Scan(&dir, &file, &lastModified)
		mod[dir+"/"+file] = lastModified
	}

	return mod
}
