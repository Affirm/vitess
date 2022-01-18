/*
Copyright 2019 The Vitess Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package vindexes

import (
	"crypto/rand"
	"fmt"
	"strconv"

	"vitess.io/vitess/go/vt/log"

	"vitess.io/vitess/go/sqltypes"
	"vitess.io/vitess/go/vt/key"
)

var (
	_ SingleColumn = (*ShiftShard)(nil)
)

// ShiftShard defines vindex that hashes an int64 to a KeyspaceId
// by using null-key DES hash. It's Unique, Reversible and
// Functional.
// Note that at once stage we used a 3DES-based hash here,
// but for a null key as in our case, they are completely equivalent.

type ShiftShard struct {
	name           string
	keys_per_shard int64
	num_shards     int8
}

// NewShiftShard creates a new ShiftShard.
func NewShiftShard(name string, m map[string]string) (Vindex, error) {
	keys_per_shard_str := m["keys_per_shard"]
	keys_per_shard, err := strconv.Atoi(keys_per_shard_str)
	if err != nil {
		return nil, err
	}
	num_shards_str := m["num_shards"]
	num_shards, err := strconv.Atoi(num_shards_str)
	if err != nil {
		return nil, err
	}
	return &ShiftShard{name: name, keys_per_shard: int64(keys_per_shard), num_shards: int8(num_shards)}, nil
}

// String returns the name of the vindex.
func (vind *ShiftShard) String() string {
	return vind.name
}

// Cost returns the cost of this index as 1.
func (vind *ShiftShard) Cost() int {
	return 1
}

// IsUnique returns true since the Vindex is unique.
func (vind *ShiftShard) IsUnique() bool {
	return true
}

// NeedsVCursor satisfies the Vindex interface.
func (vind *ShiftShard) NeedsVCursor() bool {
	return false
}

// Map can map ids to key.Destination objects.
func (vind *ShiftShard) Map(cursor VCursor, ids []sqltypes.Value) ([]key.Destination, error) {
	log.Infof("Shift Mapping: %s", ids)
	out := make([]key.Destination, len(ids))
	for i, id := range ids {
		// var num uint64
		var err error

		shardBytes := make([]byte, 1)
		if id.IsNull() {
			// If null, this is likely an insert,
			rand.Read(shardBytes)
			log.Infof("Shift: Mapped to null, randomizing shard allocation: %d", shardBytes[0])
		} else if id.IsIntegral() {
			ival, _ := id.ToInt64()
			shardId := (ival / int64(vind.keys_per_shard)) * (256 / int64(vind.num_shards))
			shardBytes[0] = uint8(shardId)
			log.Infof("Shift: IsIntegral, Id is: %d, shard Id is: %d, %d", ival, shardId, shardBytes[0])
		} else {
			log.Infof("Shift: Not integral: REKT")
			err = fmt.Errorf("ShiftShard Error: Only supporting ints bro")
		}

		if err != nil {
			out[i] = key.DestinationNone{}
			continue
		}
		out[i] = key.DestinationKeyspaceID(shardBytes)
	}
	log.Infof("Mapping out: %s")
	return out, nil
}

// Verify returns true if ids maps to ksids.
func (vind *ShiftShard) Verify(_ VCursor, ids []sqltypes.Value, ksids [][]byte) ([]bool, error) {
	log.Infof("Shift Mapping verifiction for: %s ===> %s", ids, ksids)
	out := make([]bool, len(ids))
	for i := range ids {
		// num, err := evalengine.ToUint64(ids[i])
		// if err != nil {
		// 	return nil, err
		// }
		// out[i] = bytes.Equal(vhash(num), ksids[i])
		out[i] = true
	}
	return out, nil
}

func init() {
	Register("shift", NewShiftShard)
}
