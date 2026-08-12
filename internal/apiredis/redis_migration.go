package apiredis

import (
	"log"
	"strconv"

	redis "github.com/valkey-io/valkey-go"
)

const CurrentVersion = 5

var migrationTable = map[int]func(rdb *redis.Client) error{
	1: MigrationV1, // Standardized naming
	2: MigrationV2, // Add optional conf submit scores show sandbaggers
	3: MigrationV3, // Add optional conf submit scores show rats (rollercoaster)
	4: MigrationV4, // Add optional conf sandbagger threashold
	5: MigrationV5, // Add optional conf monthly improvement threshold
}

func Migrate(rdb *redis.Client) error {
	v, err := DATA_REDIS_VERSION.Global().Get(rdb)
	if err != nil && err != redis.Nil {
		log.Println("Failed to get redis data version "+DATA_REDIS_VERSION.Name, err)
		return err
	}
	if v == "" {
		v = "0"
	}

	i, err := strconv.Atoi(v)
	if err != nil {
		log.Println("Failed to convert redis data version to int "+v, err)
		log.Println("Treating as Version 0")
		i = 0
	}
	for i < CurrentVersion {
		log.Println("Running Migration from version " + strconv.Itoa(i) + " to " + strconv.Itoa(i+1))
		err := migrationTable[i+1](rdb)
		if err != nil {
			log.Println("Failed to run Migration from version "+strconv.Itoa(i)+" to "+strconv.Itoa(i+1), err)
			return err
		}
		err = DATA_REDIS_VERSION.Global().Set(rdb, strconv.Itoa(i+1))
		if err != nil {
			log.Println("Failed to set redis data version "+DATA_REDIS_VERSION.Name, err)
			return err
		}
		i++
	}
	return nil
}
