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

package engine

import (
	"fmt"
	"time"

	"vitess.io/vitess/go/vt/vtgate/evalengine"

	topodatapb "vitess.io/vitess/go/vt/proto/topodata"

	"vitess.io/vitess/go/sqltypes"
	"vitess.io/vitess/go/vt/key"
	"vitess.io/vitess/go/vt/srvtopo"
	"vitess.io/vitess/go/vt/vterrors"
	"vitess.io/vitess/go/vt/vtgate/vindexes"

	querypb "vitess.io/vitess/go/vt/proto/query"
	vtrpcpb "vitess.io/vitess/go/vt/proto/vtrpc"
)

var _ Primitive = (*Delete)(nil)

// Delete represents the instructions to perform a delete.
type Delete struct {
	DML

	// Delete does not take inputs
	noInputs
}

var delName = map[DMLOpcode]string{
	Unsharded:     "DeleteUnsharded",
	Equal:         "DeleteEqual",
	In:            "DeleteIn",
	Scatter:       "DeleteScatter",
	ByDestination: "DeleteByDestination",
}

// RouteType returns a description of the query routing type used by the primitive
func (del *Delete) RouteType() string {
	return delName[del.Opcode]
}

// GetKeyspaceName specifies the Keyspace that this primitive routes to.
func (del *Delete) GetKeyspaceName() string {
	return del.Keyspace.Name
}

// GetTableName specifies the table that this primitive routes to.
func (del *Delete) GetTableName() string {
	if del.Table != nil {
		return del.Table.Name.String()
	}
	return ""
}

// TryExecute performs a non-streaming exec.
func (del *Delete) TryExecute(vcursor VCursor, bindVars map[string]*querypb.BindVariable, _ bool) (*sqltypes.Result, error) {
	if del.QueryTimeout != 0 {
		cancel := vcursor.SetContextTimeout(time.Duration(del.QueryTimeout) * time.Millisecond)
		defer cancel()
	}

	switch del.Opcode {
	case Unsharded:
		return del.execDeleteUnsharded(vcursor, bindVars)
	case Equal:
		switch del.Vindex.(type) {
		case vindexes.MultiColumn:
			return del.execDeleteEqualMultiCol(vcursor, bindVars)
		default:
			return del.execDeleteEqual(vcursor, bindVars)
		}
	case In:
		return del.execDeleteIn(vcursor, bindVars)
	case Scatter:
		return del.execDeleteByDestination(vcursor, bindVars, key.DestinationAllShards{})
	case ByDestination:
		return del.execDeleteByDestination(vcursor, bindVars, del.TargetDestination)
	default:
		// Unreachable.
		return nil, fmt.Errorf("unsupported opcode: %v", del)
	}
}

// TryStreamExecute performs a streaming exec.
func (del *Delete) TryStreamExecute(vcursor VCursor, bindVars map[string]*querypb.BindVariable, wantfields bool, callback func(*sqltypes.Result) error) error {
	res, err := del.TryExecute(vcursor, bindVars, wantfields)
	if err != nil {
		return err
	}
	return callback(res)
}

// GetFields fetches the field info.
func (del *Delete) GetFields(VCursor, map[string]*querypb.BindVariable) (*sqltypes.Result, error) {
	return nil, fmt.Errorf("BUG: unreachable code for %q", del.Query)
}

func (del *Delete) execDeleteUnsharded(vcursor VCursor, bindVars map[string]*querypb.BindVariable) (*sqltypes.Result, error) {
	rss, _, err := vcursor.ResolveDestinations(del.Keyspace.Name, nil, []key.Destination{key.DestinationAllShards{}})
	if err != nil {
		return nil, err
	}
	if len(rss) != 1 {
		return nil, vterrors.Errorf(vtrpcpb.Code_FAILED_PRECONDITION, "cannot send query to multiple shards for un-sharded database: %v", rss)
	}
	err = allowOnlyPrimary(rss...)
	if err != nil {
		return nil, err
	}
	return execShard(vcursor, del.Query, bindVars, rss[0], true, true /* canAutocommit */)
}

func (del *Delete) execDeleteEqual(vcursor VCursor, bindVars map[string]*querypb.BindVariable) (*sqltypes.Result, error) {
	env := evalengine.EnvWithBindVars(bindVars)
	key, err := env.Evaluate(del.Values[0])
	if err != nil {
		return nil, err
	}
	rs, ksid, err := resolveSingleShard(vcursor, del.Vindex.(vindexes.SingleColumn), del.Keyspace, key.Value())
	if err != nil {
		return nil, err
	}
	err = allowOnlyPrimary(rs)
	if err != nil {
		return nil, err
	}

	if len(ksid) == 0 {
		return &sqltypes.Result{}, nil
	}
	if del.OwnedVindexQuery != "" {
		err = del.deleteVindexEntries(vcursor, bindVars, []*srvtopo.ResolvedShard{rs})
		if err != nil {
			return nil, err
		}
	}
	return execShard(vcursor, del.Query, bindVars, rs, true /* rollbackOnError */, true /* canAutocommit */)
}

func (del *Delete) execDeleteEqualMultiCol(vcursor VCursor, bindVars map[string]*querypb.BindVariable) (*sqltypes.Result, error) {
	env := evalengine.EnvWithBindVars(bindVars)
	var rowValue []sqltypes.Value
	for _, rvalue := range del.Values {
		v, err := env.Evaluate(rvalue)
		if err != nil {
			return nil, err
		}
		rowValue = append(rowValue, v.Value())
	}
	rss, _, err := resolveShardsMultiCol(vcursor, del.Vindex.(vindexes.MultiColumn), del.Keyspace, [][]sqltypes.Value{rowValue}, false /* shardIdsNeeded */)
	if err != nil {
		return nil, err
	}
	if len(rss) != 1 {
		return nil, vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT, "vindex mapped id to multi shards: %d", len(rss))
	}
	err = allowOnlyPrimary(rss...)
	if err != nil {
		return nil, err
	}
	if del.OwnedVindexQuery != "" {
		err = del.deleteVindexEntries(vcursor, bindVars, rss)
		if err != nil {
			return nil, err
		}
	}
	return execShard(vcursor, del.Query, bindVars, rss[0], true /* rollbackOnError */, true /* canAutocommit */)
}

func (del *Delete) execDeleteIn(vcursor VCursor, bindVars map[string]*querypb.BindVariable) (*sqltypes.Result, error) {
	rss, queries, err := resolveMultiValueShards(vcursor, del.Keyspace, del.Query, bindVars, del.Values, del.Vindex)
	if err != nil {
		return nil, err
	}
	err = allowOnlyPrimary(rss...)
	if err != nil {
		return nil, err
	}

	if del.OwnedVindexQuery != "" {
		if err := del.deleteVindexEntries(vcursor, bindVars, rss); err != nil {
			return nil, err
		}
	}
	return execMultiShard(vcursor, rss, queries, del.MultiShardAutocommit)
}

func (del *Delete) execDeleteByDestination(vcursor VCursor, bindVars map[string]*querypb.BindVariable, dest key.Destination) (*sqltypes.Result, error) {
	rss, _, err := vcursor.ResolveDestinations(del.Keyspace.Name, nil, []key.Destination{dest})
	if err != nil {
		return nil, err
	}
	err = allowOnlyPrimary(rss...)
	if err != nil {
		return nil, err
	}

	queries := make([]*querypb.BoundQuery, len(rss))
	for i := range rss {
		queries[i] = &querypb.BoundQuery{
			Sql:           del.Query,
			BindVariables: bindVars,
		}
	}
	if len(del.Table.Owned) > 0 {
		err = del.deleteVindexEntries(vcursor, bindVars, rss)
		if err != nil {
			return nil, err
		}
	}
	return execMultiShard(vcursor, rss, queries, del.MultiShardAutocommit)
}

// deleteVindexEntries performs an delete if table owns vindex.
// Note: the commit order may be different from the DML order because it's possible
// for DMLs to reuse existing transactions.
func (del *Delete) deleteVindexEntries(vcursor VCursor, bindVars map[string]*querypb.BindVariable, rss []*srvtopo.ResolvedShard) error {
	queries := make([]*querypb.BoundQuery, len(rss))
	for i := range rss {
		queries[i] = &querypb.BoundQuery{Sql: del.OwnedVindexQuery, BindVariables: bindVars}
	}
	subQueryResults, errors := vcursor.ExecuteMultiShard(rss, queries, false, false)
	for _, err := range errors {
		if err != nil {
			return err
		}
	}

	if len(subQueryResults.Rows) == 0 {
		return nil
	}

	for _, row := range subQueryResults.Rows {
		ksid, err := resolveKeyspaceID(vcursor, del.KsidVindex, row[0:del.KsidLength])
		if err != nil {
			return err
		}
		colnum := del.KsidLength
		for _, colVindex := range del.Table.Owned {
			// Fetch the column values. colnum must keep incrementing.
			fromIds := make([]sqltypes.Value, 0, len(colVindex.Columns))
			for range colVindex.Columns {
				fromIds = append(fromIds, row[colnum])
				colnum++
			}
			if err := colVindex.Vindex.(vindexes.Lookup).Delete(vcursor, [][]sqltypes.Value{fromIds}, ksid); err != nil {
				return err
			}
		}
	}

	return nil
}

func (del *Delete) description() PrimitiveDescription {
	other := map[string]interface{}{
		"Query":                del.Query,
		"Table":                del.GetTableName(),
		"OwnedVindexQuery":     del.OwnedVindexQuery,
		"MultiShardAutocommit": del.MultiShardAutocommit,
		"QueryTimeout":         del.QueryTimeout,
	}

	addFieldsIfNotEmpty(del.DML, other)

	return PrimitiveDescription{
		OperatorType:     "Delete",
		Keyspace:         del.Keyspace,
		Variant:          del.Opcode.String(),
		TargetTabletType: topodatapb.TabletType_PRIMARY,
		Other:            other,
	}
}

func addFieldsIfNotEmpty(dml DML, other map[string]interface{}) {
	if dml.Vindex != nil {
		other["Vindex"] = dml.Vindex.String()
	}
	if dml.KsidVindex != nil {
		other["KsidVindex"] = dml.KsidVindex.String()
		other["KsidLength"] = dml.KsidLength
	}
	if len(dml.Values) > 0 {
		s := []string{}
		for _, value := range dml.Values {
			s = append(s, evalengine.FormatExpr(value))
		}
		other["Values"] = s
	}
}
