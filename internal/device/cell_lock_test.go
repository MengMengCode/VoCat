package device

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"vocat/internal/modem"
)

func TestSetCellLock(t *testing.T) {
	target := &CellLockTarget{EARFCN: 1650, PCI: 0}
	for _, test := range []struct {
		name, model, query, initial, command, readback string
		target                                         *CellLockTarget
	}{
		{
			name: "ML307A lock", model: "ML307A", target: &CellLockTarget{EARFCN: 600, PCI: 0},
			query: "AT+MLOCKFREQ?", initial: "+MLOCKFREQ: 0",
			command: "AT+MLOCKFREQ=1,600,0", readback: "+MLOCKFREQ: 1,600,0",
		},
		{
			name: "ML307A clear", model: "ML307A",
			query: "AT+MLOCKFREQ?", initial: "+MLOCKFREQ: 1,1650,0",
			command: "AT+MLOCKFREQ=0", readback: "+MLOCKFREQ: 0",
		},
		{
			name: "Quectel 4g overwrites unknown configuration", model: "EC20", target: target,
			query: `AT+QNWLOCK="common/4g"`, initial: `+QNWLOCK: "common/4g",2,1650,0,2400,106`,
			command: `AT+QNWLOCK="common/4g",1,1650,0`, readback: `+QNWLOCK: "common/4g",1,1650,0`,
		},
		{
			name: "Quectel 4g clear", model: "EC20",
			query: `AT+QNWLOCK="common/4g"`, initial: `+QNWLOCK: "common/4g",1,1650,0`,
			command: `AT+QNWLOCK="common/4g",0`, readback: `+QNWLOCK: "common/4g",0`,
		},
		{
			name: "Quectel lte lock", model: "EC25", target: target,
			query: `AT+QNWLOCK="common/lte"`, initial: `+QNWLOCK: "common/lte",0,0,0,0`,
			command: `AT+QNWLOCK="common/lte",2,1650,0`, readback: `+QNWLOCK: "common/lte",2,1650,0,0`,
		},
		{
			name: "Quectel lte clears configuration before completion", model: "EC25",
			query: `AT+QNWLOCK="common/lte"`, initial: `+QNWLOCK: "common/lte",2,1650,0,0`,
			command: `AT+QNWLOCK="common/lte",0`, readback: `+QNWLOCK: "common/lte",0,1650,0,1`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var steps []clientStep
			if test.query == `AT+QNWLOCK="common/lte"` {
				steps = append(steps, rejectedCommand(`AT+QNWLOCK="common/4g"`))
			}
			steps = append(steps,
				clientStep{command: test.query, response: okResponse(test.initial)},
				clientStep{command: test.command, response: okResponse()},
				clientStep{command: test.query, response: okResponse(test.readback)},
			)
			client := &transcriptClient{steps: steps}
			manager, id := newStartedTestManager(t, client)
			manager.devices[id].candidate.Product = test.model
			status, err := manager.SetCellLock(context.Background(), id, test.target)
			if err != nil || !reflect.DeepEqual(status.Target, test.target) {
				t.Fatalf("target=%+v error=%v, want %+v", status.Target, err, test.target)
			}
			client.assertDone(t)
		})
	}
}

func TestQuectelLTERejectsMalformedStatus(t *testing.T) {
	for _, line := range []string{
		`+QNWLOCK: "common/lte",2,1650,0`,
		`+QNWLOCK: "common/lte",0,1650,0,2`,
	} {
		if _, err := quectelLTELockDialect.parseStatus(okResponse(line)); err == nil {
			t.Fatalf("accepted malformed LTE response %q", line)
		}
	}
}

func TestSetCellLockFailures(t *testing.T) {
	const command = "AT+MLOCKFREQ=1,1650,0"
	for _, test := range []struct {
		name   string
		setter clientStep
		closes int
	}{
		{name: "setter rejected", setter: rejectedCommand(command)},
		{name: "missing final", setter: clientStep{command: command, err: context.DeadlineExceeded}, closes: 1},
		{name: "readback mismatch", setter: clientStep{command: command, response: okResponse()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			steps := []clientStep{
				{command: "AT+MLOCKFREQ?", response: okResponse("+MLOCKFREQ: 0")},
				test.setter,
			}
			if test.setter.err == nil {
				steps = append(steps, clientStep{command: "AT+MLOCKFREQ?", response: okResponse("+MLOCKFREQ: 1,1650,1")})
			}
			client := &transcriptClient{steps: steps}
			manager, id := newStartedTestManager(t, client)
			manager.devices[id].candidate.Product = "ML307A"
			_, err := manager.SetCellLock(context.Background(), id, &CellLockTarget{1650, 0})
			if err == nil {
				t.Fatal("unconfirmed write succeeded")
			}
			if test.setter.err != nil && !errors.Is(err, test.setter.err) {
				t.Fatalf("lost modem error: %v", err)
			}
			if client.closeCount != test.closes {
				t.Fatalf("client closed %d times, want %d", client.closeCount, test.closes)
			}
			client.assertDone(t)
		})
	}
}

func TestCells(t *testing.T) {
	serving := CellLockTarget{EARFCN: 1650, PCI: 0}
	neighbor := CellLockTarget{EARFCN: 2400, PCI: 106}
	quectelServing := clientStep{
		command:  `AT+QENG="servingcell"`,
		response: okResponse(`+QENG: "servingcell","NOCONN","LTE","FDD",460,01,5F1E805,0,1650,3,5,5,8340,-97,-10,-68,15,9`),
	}
	for _, test := range []struct {
		name, model, neighborsStatus string
		steps                        []clientStep
		targets                      []CellLockTarget
	}{
		{
			name: "ML307A serving cell", model: "ML307", neighborsStatus: "unsupported",
			steps: []clientStep{{
				command:  `AT+MUESTATS="cell"`,
				response: okResponse(`+MUESTATS: "scell",4,460,01,0,,0,-970,-100,-680,150,20.0`),
			}},
			targets: []CellLockTarget{{EARFCN: 0, PCI: 0}},
		},
		{
			name: "Quectel merges serving and neighbors", model: "EC20", neighborsStatus: "available",
			steps: []clientStep{quectelServing, {
				command: `AT+QENG="neighbourcell"`,
				response: okResponse(
					`+QENG: "neighbourcell intra","LTE",1650,0,-12,-101,-70,10`,
					`+QENG: "neighbourcell inter","LTE",2400,106,-12,-101`,
				),
			}},
			targets: []CellLockTarget{serving, neighbor},
		},
		{
			name: "Quectel serving without neighbor support", model: "EC25", neighborsStatus: "unavailable",
			steps:   []clientStep{quectelServing, rejectedCommand(`AT+QENG="neighbourcell"`)},
			targets: []CellLockTarget{serving},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &transcriptClient{steps: test.steps}
			manager, id := newStartedTestManager(t, client)
			manager.devices[id].candidate.Product = test.model
			cells, err := manager.Cells(context.Background(), id)
			if err != nil || len(cells.Items) != len(test.targets) || cells.NeighborsStatus != test.neighborsStatus {
				t.Fatalf("cells=%+v error=%v", cells, err)
			}
			for i, want := range test.targets {
				if got := cells.Items[i]; got.EARFCN != want.EARFCN || got.PCI != want.PCI {
					t.Errorf("cell %d = %+v, want %+v", i, got, want)
				}
			}
			first := cells.Items[0]
			if first.Source != "serving" || first.PLMN != "46001" || first.RSRP == nil || *first.RSRP != -97 {
				t.Fatalf("serving identity/measurement was lost: %+v", first)
			}
			client.assertDone(t)
		})
	}
}

func TestCellLockRejectsUnsupportedDevice(t *testing.T) {
	client := &transcriptClient{}
	manager, id := newStartedTestManager(t, client)
	manager.devices[id].candidate.VendorID = "unknown"
	manager.devices[id].candidate.Product = "unknown"
	ctx := context.Background()
	_, cellsErr := manager.Cells(ctx, id)
	_, statusErr := manager.CellLock(ctx, id)
	_, lockErr := manager.SetCellLock(ctx, id, &CellLockTarget{1650, 0})
	_, clearErr := manager.SetCellLock(ctx, id, nil)
	for _, err := range []error{cellsErr, statusErr, lockErr, clearErr} {
		if !errors.Is(err, ErrUnsupportedCapability) {
			t.Fatalf("expected unsupported capability, got %v", err)
		}
	}
	if got := manager.opener.(*staticOpener).openCount; got != 0 {
		t.Fatalf("opened unsupported device %d times", got)
	}
	client.assertDone(t)
}

func TestServingRecordSnapshotCompatibility(t *testing.T) {
	mue := parseMUESTATSCell(okResponse(`+MUESTATS: "scell",4,460,01,1650,,106,-810`))
	if mue.AccessTech != "LTE" || mue.Channel != "1650" || mue.RSRP != nil {
		t.Fatalf("partial MUESTATS snapshot = %+v", mue)
	}

	nonLTE := parseQENG(okResponse(`+QENG: "servingcell","NOCONN","NR5G"`))
	if nonLTE.AccessTech != "NR5G" || nonLTE.Channel != "" || nonLTE.RSRP != nil {
		t.Fatalf("non-LTE QENG snapshot = %+v", nonLTE)
	}

	partial := parseQENG(okResponse(`+QENG: "servingcell","NOCONN","LTE"`))
	if partial.AccessTech != "LTE" || partial.Channel != "" || partial.RSRP != nil {
		t.Fatalf("partial QENG snapshot = %+v", partial)
	}
}

func rejectedCommand(command string) clientStep {
	return clientStep{
		command:  command,
		response: modem.Response{Final: "ERROR"},
		err:      &modem.CommandError{Command: command, Final: "ERROR"},
	}
}
