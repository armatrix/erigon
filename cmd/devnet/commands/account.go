package commands

import (
	"fmt"
	"github.com/ledgerwatch/erigon/cmd/devnet/models"
	"github.com/ledgerwatch/erigon/cmd/devnet/requests"

	"github.com/ledgerwatch/erigon/common"
)

const (
	addr     = "0x71562b71999873DB5b286dF957af199Ec94617F7"
	ethValue = 10000
)

func callGetBalance(addr, blockNum string, checkBal uint64) {
	fmt.Printf("Getting balance for address: %q...\n", addr)
	address := common.HexToAddress(addr)
	bal, err := requests.GetBalance(models.ReqId, address, blockNum)
	if err != nil {
		fmt.Printf("FAILURE => %v\n", err)
		return
	}

	if checkBal > 0 && checkBal != bal {
		fmt.Printf("FAILURE => Balance should be %d, got %d\n", checkBal, bal)
		return
	}

	fmt.Printf("SUCCESS => Balance: %d\n", bal)
}
