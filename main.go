package main

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/misc/eip4844"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/joho/godotenv"
)

var scrollChainMetaData = &bind.MetaData{
	ABI: "[{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"uint256\",\"name\":\"batchIndex\",\"type\":\"uint256\"},{\"indexed\":true,\"internalType\":\"bytes32\",\"name\":\"batchHash\",\"type\":\"bytes32\"}],\"name\":\"CommitBatch\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"uint256\",\"name\":\"batchIndex\",\"type\":\"uint256\"},{\"indexed\":true,\"internalType\":\"bytes32\",\"name\":\"batchHash\",\"type\":\"bytes32\"},{\"indexed\":false,\"internalType\":\"bytes32\",\"name\":\"stateRoot\",\"type\":\"bytes32\"},{\"indexed\":false,\"internalType\":\"bytes32\",\"name\":\"withdrawRoot\",\"type\":\"bytes32\"}],\"name\":\"FinalizeBatch\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"uint256\",\"name\":\"batchIndex\",\"type\":\"uint256\"},{\"indexed\":true,\"internalType\":\"bytes32\",\"name\":\"batchHash\",\"type\":\"bytes32\"}],\"name\":\"RevertBatch\",\"type\":\"event\"},{\"inputs\":[{\"internalType\":\"uint8\",\"name\":\"version\",\"type\":\"uint8\"},{\"internalType\":\"bytes\",\"name\":\"parentBatchHeader\",\"type\":\"bytes\"},{\"internalType\":\"bytes[]\",\"name\":\"chunks\",\"type\":\"bytes[]\"},{\"internalType\":\"bytes\",\"name\":\"skippedL1MessageBitmap\",\"type\":\"bytes\"}],\"name\":\"commitBatch\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"batchIndex\",\"type\":\"uint256\"}],\"name\":\"committedBatches\",\"outputs\":[{\"internalType\":\"bytes32\",\"name\":\"\",\"type\":\"bytes32\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"bytes\",\"name\":\"batchHeader\",\"type\":\"bytes\"},{\"internalType\":\"bytes32\",\"name\":\"prevStateRoot\",\"type\":\"bytes32\"},{\"internalType\":\"bytes32\",\"name\":\"postStateRoot\",\"type\":\"bytes32\"},{\"internalType\":\"bytes32\",\"name\":\"withdrawRoot\",\"type\":\"bytes32\"},{\"internalType\":\"bytes\",\"name\":\"aggrProof\",\"type\":\"bytes\"}],\"name\":\"finalizeBatchWithProof\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"batchIndex\",\"type\":\"uint256\"}],\"name\":\"finalizedStateRoots\",\"outputs\":[{\"internalType\":\"bytes32\",\"name\":\"\",\"type\":\"bytes32\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"batchIndex\",\"type\":\"uint256\"}],\"name\":\"isBatchFinalized\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"lastFinalizedBatchIndex\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"bytes\",\"name\":\"batchHeader\",\"type\":\"bytes\"},{\"internalType\":\"uint256\",\"name\":\"count\",\"type\":\"uint256\"}],\"name\":\"revertBatch\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"batchIndex\",\"type\":\"uint256\"}],\"name\":\"withdrawRoots\",\"outputs\":[{\"internalType\":\"bytes32\",\"name\":\"\",\"type\":\"bytes32\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
}

const numBlocksToFetch = 1000
const batchSize = 10

type CommitBatchEvent struct {
	BatchIndex *big.Int
	BatchHash  common.Hash
}

type FinalizeBatchEvent struct {
	BatchIndex   *big.Int
	BatchHash    common.Hash
	StateRoot    common.Hash
	WithdrawRoot common.Hash
}

func main() {
	glogger := log.NewGlogHandler(log.NewTerminalHandler(os.Stderr, true))
	glogger.Verbosity(log.LevelInfo)
	log.SetDefault(log.NewLogger(glogger))

	err := godotenv.Load(".env")
	if err != nil {
		log.Crit("failed to load .env file", "err", err)
	}

	client, err := ethclient.Dial(os.Getenv("RPC_PROVIDER_URL"))
	if err != nil {
		log.Crit("failed to connect to network", "err", err)
	}

	latestSafeBlock, err := client.HeaderByNumber(context.Background(), nil)
	if err != nil {
		log.Crit("failed to get latest safe block header", "err", err)
	}
	latestSafeBlockNumber := latestSafeBlock.Number.Uint64()

	startTime := time.Now()

	for i := latestSafeBlockNumber; i > latestSafeBlockNumber-numBlocksToFetch; i -= batchSize {
		from := i - batchSize + 1
		to := i

		log.Info("Fetching block headers", "from", from, "to", to)

		logs, err := fetchL1EventLogs(context.Background(), from, to, client)
		if err != nil {
			log.Error("Failed to fetch L1 event logs", "err", err)
			continue
		}

		err = parseL1BatchEventLogs(context.Background(), logs, client)
		if err != nil {
			log.Error("Failed to parse L1 batch event logs", "err", err)
			continue
		}
	}

	elapsedTime := time.Since(startTime)
	log.Info("Finished fetching and parsing L1 batch event logs", "elapsedTime", elapsedTime)
}

func fetchL1EventLogs(ctx context.Context, from, to uint64, client *ethclient.Client) ([]types.Log, error) {
	scrollChainABI, _ := scrollChainMetaData.GetAbi()

	query := ethereum.FilterQuery{
		FromBlock: new(big.Int).SetUint64(from), // inclusive
		ToBlock:   new(big.Int).SetUint64(to),   // inclusive
		Addresses: []common.Address{common.HexToAddress("0xa13BAF47339d63B743e7Da8741db5456DAc1E556")},
		Topics: [][]common.Hash{{
			scrollChainABI.Events["CommitBatch"].ID,
			scrollChainABI.Events["FinalizeBatch"].ID,
		}},
	}

	eventLogs, err := client.FilterLogs(ctx, query)
	if err != nil {
		log.Error("Failed to filter L1 event logs", "from", from, "to", to, "err", err)
		return nil, err
	}
	return eventLogs, nil
}

func parseL1BatchEventLogs(ctx context.Context, logs []types.Log, client *ethclient.Client) error {
	scrollChainABI, _ := scrollChainMetaData.GetAbi()

	for _, vlog := range logs {
		switch vlog.Topics[0] {
		case scrollChainABI.Events["CommitBatch"].ID:
			event := CommitBatchEvent{}
			if err := unpackLog(scrollChainABI, &event, "CommitBatch", vlog); err != nil {
				log.Error("Failed to unpack CommitBatch event", "err", err)
				return err
			}
			commitBatchTx, isPending, err := client.TransactionByHash(ctx, vlog.TxHash)
			if err != nil || isPending {
				log.Error("Failed to get commit batch tx or the tx is still pending", "err", err, "isPending", isPending)
				return err
			}
			receipt, err := client.TransactionReceipt(ctx, vlog.TxHash)
			if err != nil {
				log.Error("Failed to get commit batch tx receipt", "err", err)
				return err
			}
			header, err := client.HeaderByNumber(context.Background(), receipt.BlockNumber)
			if err != nil {
				log.Warn("failed to get block header", "blockNumber", receipt.BlockNumber, "err", err)
				continue
			}
			blobBaseFee := eip4844.CalcBlobFee(*header.ExcessBlobGas)
			log.Info("CommitBatch event",
				"batchIndex", event.BatchIndex,
				"batchHash", event.BatchHash.Hex(),
				"txFee", commitBatchTx.Cost(),
				"baseFee", header.BaseFee,
				"blobBaseFee", blobBaseFee,
			)

		case scrollChainABI.Events["FinalizeBatch"].ID:
			event := FinalizeBatchEvent{}
			if err := unpackLog(scrollChainABI, &event, "FinalizeBatch", vlog); err != nil {
				log.Error("Failed to unpack FinalizeBatch event", "err", err)
				return err
			}
			finalizeBatchTx, isPending, err := client.TransactionByHash(ctx, vlog.TxHash)
			if err != nil || isPending {
				log.Error("Failed to get finalize batch tx or the tx is still pending", "err", err, "isPending", isPending)
				return err
			}
			receipt, err := client.TransactionReceipt(ctx, vlog.TxHash)
			if err != nil {
				log.Error("Failed to get finalize batch tx receipt", "err", err)
				return err
			}
			header, err := client.HeaderByNumber(context.Background(), receipt.BlockNumber)
			if err != nil {
				log.Warn("failed to get block header", "blockNumber", receipt.BlockNumber, "err", err)
				continue
			}
			blobBaseFee := eip4844.CalcBlobFee(*header.ExcessBlobGas)
			log.Info("FinalizeBatch event",
				"batchIndex", event.BatchIndex,
				"batchHash", event.BatchHash.Hex(),
				"txFee", finalizeBatchTx.Cost(),
				"baseFee", header.BaseFee,
				"blobBaseFee", blobBaseFee,
			)
		}
	}
	return nil
}

func unpackLog(c *abi.ABI, out interface{}, event string, log types.Log) error {
	if log.Topics[0] != c.Events[event].ID {
		return fmt.Errorf("event signature mismatch")
	}
	if len(log.Data) > 0 {
		if err := c.UnpackIntoInterface(out, event, log.Data); err != nil {
			return err
		}
	}
	var indexed abi.Arguments
	for _, arg := range c.Events[event].Inputs {
		if arg.Indexed {
			indexed = append(indexed, arg)
		}
	}
	return abi.ParseTopics(out, indexed, log.Topics[1:])
}
