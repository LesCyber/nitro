package timeboost

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/secp256k1"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/offchainlabs/nitro/solgen/go/express_lane_auctiongen"
	"github.com/offchainlabs/nitro/util/containers"
	"github.com/offchainlabs/nitro/util/stopwaiter"
	"github.com/pkg/errors"
)

type BidderClient struct {
	stopwaiter.StopWaiter
	chainId                *big.Int
	name                   string
	auctionContractAddress common.Address
	txOpts                 *bind.TransactOpts
	client                 *ethclient.Client
	privKey                *ecdsa.PrivateKey
	auctionContract        *express_lane_auctiongen.ExpressLaneAuction
	auctioneerClient       *rpc.Client
	initialRoundTimestamp  time.Time
	roundDuration          time.Duration
	domainValue            []byte
}

// TODO: Provide a safer option.
type Wallet struct {
	TxOpts  *bind.TransactOpts
	PrivKey *ecdsa.PrivateKey
}

func NewBidderClient(
	ctx context.Context,
	name string,
	wallet *Wallet,
	client *ethclient.Client,
	auctionContractAddress common.Address,
	auctioneerEndpoint string,
) (*BidderClient, error) {
	chainId, err := client.ChainID(ctx)
	if err != nil {
		return nil, err
	}
	auctionContract, err := express_lane_auctiongen.NewExpressLaneAuction(auctionContractAddress, client)
	if err != nil {
		return nil, err
	}
	roundTimingInfo, err := auctionContract.RoundTimingInfo(&bind.CallOpts{})
	if err != nil {
		return nil, err
	}
	initialTimestamp := time.Unix(int64(roundTimingInfo.OffsetTimestamp), 0)
	roundDuration := time.Duration(roundTimingInfo.RoundDurationSeconds) * time.Second

	auctioneerClient, err := rpc.DialContext(ctx, auctioneerEndpoint)
	if err != nil {
		return nil, err
	}
	return &BidderClient{
		chainId:                chainId,
		name:                   name,
		auctionContractAddress: auctionContractAddress,
		client:                 client,
		txOpts:                 wallet.TxOpts,
		privKey:                wallet.PrivKey,
		auctionContract:        auctionContract,
		auctioneerClient:       auctioneerClient,
		initialRoundTimestamp:  initialTimestamp,
		roundDuration:          roundDuration,
		domainValue:            domainValue,
	}, nil
}

func (bd *BidderClient) Start(ctx_in context.Context) {
	bd.StopWaiter.Start(ctx_in, bd)
}

func (bd *BidderClient) Deposit(ctx context.Context, amount *big.Int) error {
	tx, err := bd.auctionContract.Deposit(bd.txOpts, amount)
	if err != nil {
		return err
	}
	receipt, err := bind.WaitMined(ctx, bd.client, tx)
	if err != nil {
		return err
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return errors.New("deposit failed")
	}
	return nil
}

func (bd *BidderClient) Bid(
	ctx context.Context, amount *big.Int, expressLaneController common.Address,
) (*Bid, error) {
	newBid := &Bid{
		ChainId:                bd.chainId,
		ExpressLaneController:  expressLaneController,
		AuctionContractAddress: bd.auctionContractAddress,
		Round:                  CurrentRound(bd.initialRoundTimestamp, bd.roundDuration) + 1,
		Amount:                 amount,
		Signature:              nil,
	}
	sig, err := sign(newBid.ToMessageBytes(), bd.privKey)
	if err != nil {
		return nil, err
	}
	newBid.Signature = sig
	promise := bd.submitBid(newBid)
	if _, err := promise.Await(ctx); err != nil {
		return nil, err
	}
	return newBid, nil
}

func (bd *BidderClient) submitBid(bid *Bid) containers.PromiseInterface[struct{}] {
	return stopwaiter.LaunchPromiseThread[struct{}](bd, func(ctx context.Context) (struct{}, error) {
		err := bd.auctioneerClient.CallContext(ctx, nil, "auctioneer_submitBid", bid.ToJson())
		return struct{}{}, err
	})
}

func sign(message []byte, key *ecdsa.PrivateKey) ([]byte, error) {
	prefixed := crypto.Keccak256(append([]byte(fmt.Sprintf("\x19Ethereum Signed Message:\n%d", len(message))), message...))
	sig, err := secp256k1.Sign(prefixed, math.PaddedBigBytes(key.D, 32))
	if err != nil {
		return nil, err
	}
	sig[64] += 27
	return sig, nil
}
