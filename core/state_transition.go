// Copyright 2014 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"encoding/json"

	"github.com/PlatONnetwork/PlatON-Go/core/types"

	"github.com/PlatONnetwork/PlatON-Go/common"
	"github.com/PlatONnetwork/PlatON-Go/core/vm"
	"github.com/PlatONnetwork/PlatON-Go/log"
	"github.com/PlatONnetwork/PlatON-Go/params"
	"github.com/PlatONnetwork/PlatON-Go/rlp"
	"github.com/PlatONnetwork/PlatON-Go/core/state"
)

var (
	errInsufficientBalanceForGas = errors.New("insufficient balance to pay for gas")
)

var CnsManagerAddr string = "0x0000000000000000000000000000000000000011"

/*
A state transition is a change made when a transaction is applied to the current world state
The state transitioning model does all the necessary work to work out a valid new state root.
1) Nonce handling
2) Pre pay gas
3) Create a new state object if the recipient is \0*32
4) Value transfer
== If contract creation ==
  4a) Attempt to run transaction data
  4b) If valid, use result as code for the new state object
== end ==
5) Run Script section
6) Derive new state root
*/
type StateTransition struct {
	gp         *GasPool
	msg        Message
	gas        uint64
	gasPrice   *big.Int
	initialGas uint64
	value      *big.Int
	data       []byte
	state      vm.StateDB
	evm        *vm.EVM
}

// Message represents a message sent to a contract.
type Message interface {
	From() common.Address
	//FromFrontier() (common.Address, error)
	To() *common.Address
	SetTo(common.Address)
	SetData([]byte)
	TxType() uint64
	SetTxType(uint64)
	SetNonce(uint64)

	GasPrice() *big.Int
	Gas() uint64
	Value() *big.Int

	Nonce() uint64
	CheckNonce() bool
	Data() []byte
}

// IntrinsicGas computes the 'intrinsic gas' for a message with the given data.
func IntrinsicGas(data []byte, contractCreation, homestead bool) (uint64, error) {
	// Set the starting gas for the raw transaction
	var gas uint64
	if contractCreation && homestead {
		gas = params.TxGasContractCreation
	} else {
		gas = params.TxGas
	}
	// Bump the required gas by the amount of transactional data
	if len(data) > 0 {
		// Zero and non-zero bytes are priced differently
		var nz uint64
		for _, byt := range data {
			if byt != 0 {
				nz++
			}
		}
		// Make sure we don't exceed uint64 for all data combinations
		if (math.MaxUint64-gas)/params.TxDataNonZeroGas < nz {
			return 0, vm.ErrOutOfGas
		}
		gas += nz * params.TxDataNonZeroGas

		z := uint64(len(data)) - nz
		if (math.MaxUint64-gas)/params.TxDataZeroGas < z {
			return 0, vm.ErrOutOfGas
		}
		gas += z * params.TxDataZeroGas
	}
	return gas, nil
}

// NewStateTransition initialises and returns a new state transition object.
func NewStateTransition(evm *vm.EVM, msg Message, gp *GasPool) *StateTransition {
	return &StateTransition{
		gp:       gp,
		evm:      evm,
		msg:      msg,
		gasPrice: msg.GasPrice(),
		value:    msg.Value(),
		data:     msg.Data(),
		state:    evm.StateDB,
	}
}

func GetCnsAddr(evm *vm.EVM, msg Message, cnsName string) (*common.Address, error) {
	// TODO: 合约管理合约地址，后面设置为全局变量

	if CnsManagerAddr == "" {
		str := evm.GetStateDB().GetState(common.Address{}, []byte("cnsManager"))
		if string(str) == "" {
			return &common.Address{}, nil
		}
		CnsManagerAddr = string(str)
	}

	if cnsName == "cnsManager" {
		addrProxy := common.HexToAddress(CnsManagerAddr)
		return &addrProxy, nil
	}

	addrProxy := common.HexToAddress(CnsManagerAddr)

	fmt.Println(addrProxy.String())
	fmt.Println(cnsName)

	var contractName, contractVer string

	i := strings.Index(cnsName, ":")
	if i == -1 {
		contractName = cnsName
		contractVer = ""
	} else {
		contractName = cnsName[:i]
		contractVer = cnsName[i+1:]
	}

	paramArr := [][]byte{
		common.Int64ToBytes(int64(types.NormalTxType)),
		[]byte("getContractAddress"),
		[]byte(contractName),
		[]byte(contractVer),
	}
	paramBytes, _ := rlp.EncodeToBytes(paramArr)

	cnsMsg := types.NewMessage(msg.From(), &addrProxy, 0, new(big.Int), 0x99999, msg.GasPrice(), paramBytes, false, types.CnsTxType)
	gp := new(GasPool).AddGas(math.MaxUint64 / 2)
	ret, _, _, err := NewStateTransition(evm, cnsMsg, gp).TransitionDb()
	if err != nil {
		fmt.Println("\n\n vm applyMessage failed", err)
		return nil, nil
	}
	retStr := string(ret)
	toAddrStr := string(retStr[strings.Index(retStr, "0x"):])
	ToAddr := common.HexToAddress(toAddrStr)

	return &ToAddr, nil
}

// ApplyMessage computes the new state by applying the given message
// against the old state within the environment.
//
// ApplyMessage returns the bytes returned by any EVM execution (if it took place),
// the gas used (which includes gas refunds) and an error if it failed. An error always
// indicates a core error meaning that the message would always fail for that particular
// state and would never be accepted within a block.
func ApplyMessage(evm *vm.EVM, msg Message, gp *GasPool) ([]byte, uint64, bool, error) {

	if msg.TxType() == types.CnsTxType {

		cnsRawData := msg.Data()
		var cnsData [][]byte

		if err := rlp.DecodeBytes(cnsRawData, &cnsData); err != nil {
			return nil, 0, false, err
		}

		toAddr, err := GetCnsAddr(evm, msg, string(cnsData[1]))
		if err != nil {
			return nil, 0, false, err
		}
		msg.SetTo(*toAddr)

		cnsData = append(cnsData[:1], cnsData[2:]...)
		cnsRawData, _ = rlp.EncodeToBytes(cnsData)

		msg.SetData(cnsRawData)
		msg.SetTxType(types.NormalTxType)

		nonce := evm.StateDB.GetNonce(msg.From())
		fmt.Println("Mid nonce is ", nonce)

		msg.SetNonce(nonce)
	}
	fmt.Println()
	return NewStateTransition(evm, msg, gp).TransitionDb()
}

// to returns the recipient of the message.
func (st *StateTransition) to() common.Address {
	if st.msg == nil || st.msg.To() == nil /* contract creation */ {
		return common.Address{}
	}
	return *st.msg.To()
}

func (st *StateTransition) useGas(amount uint64) error {
	if st.gas < amount {
		return vm.ErrOutOfGas
	}
	st.gas -= amount

	return nil
}

func (st *StateTransition) buyGas() error {
	mgval := new(big.Int).Mul(new(big.Int).SetUint64(st.msg.Gas()), st.gasPrice)
	if st.state.GetBalance(st.msg.From()).Cmp(mgval) < 0 {
		return errInsufficientBalanceForGas
	}
	if err := st.gp.SubGas(st.msg.Gas()); err != nil {
		return err
	}
	st.gas += st.msg.Gas()

	st.initialGas = st.msg.Gas()
	st.state.SubBalance(st.msg.From(), mgval)
	return nil
}

func (st *StateTransition) preCheck() error {
	// Make sure this transaction's nonce is correct.
	if st.msg.CheckNonce() {
		nonce := st.state.GetNonce(st.msg.From())
		if nonce < st.msg.Nonce() {
			return ErrNonceTooHigh
		} else if nonce > st.msg.Nonce() {
			return ErrNonceTooLow
		}
	}
	return st.buyGas()
}

func fwCheck(stateDb vm.StateDB, contractAddr common.Address, caller common.Address) bool{
	var fwStatus state.FwStatus
	fwStatus = stateDb.GetFwStatus(contractAddr)
	if fwStatus.FwActive == false {
		return true
	}

	if fwStatus.ContractAddress != contractAddr {
		return false
	}

	for _, d := range fwStatus.DeniedList {
		if d == caller {
			return false
		}
	}

	for _, a := range fwStatus.AcceptedList {
		if a == caller {
			return true
		}
	}

	return false
}

func fwProcess(stateDb vm.StateDB, contractAddr common.Address, caller common.Address, input []byte) ([]byte, uint64, error){

	var fwStatus state.FwStatus
	var err error
    if stateDb.GetContractCreator(contractAddr) != caller {
        return nil, 0, err
	}

	var fwData [][]byte
	if err = rlp.DecodeBytes(input, &fwData); err != nil {
		return nil, 0, err
	}

	funcName := string(fwData[1])
	listName := string(fwData[2])
	var act state.Action
	if listName == "Accepted List" {
		act = state.ACCEPTED
	} else {
		act = state.DENIED
	}

	var list []common.Address
	var address common.Address
	l := strings.Split(string(fwData[3]), "|")
	for _, addr := range l {
		address = common.HexToAddress(addr)
		list = append(list, address)
	}

	switch funcName {
	case "__sys_FwOpen":
		stateDb.OpenFirewall(contractAddr)
	case "__sys_FwClose":
		stateDb.CloseFirewall(contractAddr)
	case "__sys_FwAdd":
		stateDb.FwAdd(contractAddr, act, list)
	case "__sys_FwClear":
		stateDb.FwClear(contractAddr, act)
	case "__sys_FwDel":
		stateDb.FwDel(contractAddr, act, list)
	case "__sys_FwSet":
		stateDb.FwSet(contractAddr, act, list)
	default:
		// "__sys_FwStatus"
		fwStatus = stateDb.GetFwStatus(contractAddr)
	}

	var returnBytes []byte
	returnBytes, err = json.Marshal(fwStatus)
	if err != nil {
		fmt.Println("fwStatus Marshal error:", err)
	}

	strHash := common.BytesToHash(common.Int32ToBytes(32))
	sizeHash := common.BytesToHash(common.Int64ToBytes(int64((len(returnBytes)))))
	var dataRealSize = len(returnBytes)
	if (dataRealSize % 32) != 0 {
		dataRealSize = dataRealSize + (32 - (dataRealSize % 32))
	}
	dataByt := make([]byte, dataRealSize)
	copy(dataByt[0:], returnBytes)

	finalData := make([]byte, 0)
	finalData = append(finalData, strHash.Bytes()...)
	finalData = append(finalData, sizeHash.Bytes()...)
	finalData = append(finalData, dataByt...)	

	return finalData, 0, err
}

// TransitionDb will transition the state by applying the current message and
// returning the result including the used gas. It returns an error if failed.
// An error indicates a consensus issue.
func (st *StateTransition) TransitionDb() (ret []byte, usedGas uint64, failed bool, err error) {
	// init initialGas value = txMsg.gas
	if err = st.preCheck(); err != nil {
		return
	}
	msg := st.msg
	sender := vm.AccountRef(msg.From())
	homestead := st.evm.ChainConfig().IsHomestead(st.evm.BlockNumber)
	contractCreation := msg.To() == nil

	// Pay intrinsic gas
	gas, err := IntrinsicGas(st.data, contractCreation, homestead)
	if err != nil {
		return nil, 0, false, err
	}
	if err = st.useGas(gas); err != nil {
		return nil, 0, false, err
	}

	var (
		evm = st.evm
		// vm errors do not effect consensus and are therefor
		// not assigned to err, except for insufficient balance
		// error.
		vmerr error
	)
	if contractCreation {
		ret, _, st.gas, vmerr = evm.Create(sender, st.data, st.gas, st.value)
	} else {
		if !fwCheck(evm.StateDB, st.to(), msg.From()) {
			log.Debug("Calling contract was refused by firewall", "err", vmerr)
		} else {
			// Increment the nonce for the next transaction
			// If the transaction is cns-type, do not increment the nonce
			if msg.TxType() != types.CnsTxType {
				st.state.SetNonce(msg.From(), st.state.GetNonce(sender.Address())+1)
			}
			if msg.TxType() == types.FwTxType {
				ret, st.gas, vmerr = fwProcess(evm.StateDB, st.to(), msg.From(), msg.Data())
			} else {
				ret, st.gas, vmerr = evm.Call(sender, st.to(), st.data, st.gas, st.value)
			}
		}
	}
	if vmerr != nil {
		log.Debug("VM returned with error", "err", vmerr)
		// The only possible consensus-error would be if there wasn't
		// sufficient balance to make the transfer happen. The first
		// balance transfer may never fail.
		if vmerr == vm.ErrInsufficientBalance {
			return nil, 0, false, vmerr
		}
	}
	st.refundGas()
	st.state.AddBalance(st.evm.Coinbase, new(big.Int).Mul(new(big.Int).SetUint64(st.gasUsed()), st.gasPrice))

	return ret, st.gasUsed(), vmerr != nil, err
}

func (st *StateTransition) refundGas() {
	// Apply refund counter, capped to half of the used gas.
	refund := st.gasUsed() / 2
	if refund > st.state.GetRefund() {
		refund = st.state.GetRefund()
	}
	st.gas += refund

	// Return ETH for remaining gas, exchanged at the original rate.
	remaining := new(big.Int).Mul(new(big.Int).SetUint64(st.gas), st.gasPrice)
	st.state.AddBalance(st.msg.From(), remaining)

	// Also return remaining gas to the block gas counter so it is
	// available for the next transaction.
	st.gp.AddGas(st.gas)
}

// gasUsed returns the amount of gas used up by the state transition.
func (st *StateTransition) gasUsed() uint64 {
	return st.initialGas - st.gas
}
