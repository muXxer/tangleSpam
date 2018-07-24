package main

import (
	"net/http"
	"strings"
	"time"

	"./logs"
	"./utils"

	"github.com/iotaledger/giota"
	"github.com/muxxer/powsrv"
)

const (
	NodeListURL       = "http://iotaspam.com/api/nodelist/nodelist.json"                                    // Link to the iotaspam list
	MWM               = 14                                                                                  // MinWeightMagnitude
	Depth             = 6                                                                                   // Depth of the Walker
	Message           = "tangleSpam by muXxer"                                                              // Message of the Tx
	Address           = "TANGLE9SPAM9999999999999999999999999999999999999999999999999999999999999999999999" // Tx Address
	Tag               = "99TANGLE99SPAM9999999999999"                                                       // Tag of the Tx
	SpamCount         = 60000                                                                               // How much Tx should be spammed
	UseRemoteNodeList = false                                                                               // Use the nodes provided in NodeListURL
	ReferenceLastTips = false                                                                               // Use the last Branch tip as the next Trunk tip
	UsePowSrv         = true                                                                                // Use the powSrv
	PowSrvPath        = "/tmp/powSrv.sock"                                                                  // Path to the powSrv
	HostPOW           = "http://localhost:14265"                                                            // Node that is used for attachToTangle Request (if UsePowSrv==false)
	CheckNodeHealth   = false                                                                               // Check the health of your nodes (connection & milestone)
	SpamIRI           = false                                                                               // Spam the
	ParallelLevel     = 5                                                                                   // How much POW can be done in parallel by powSrv
	PowTest           = false                                                                               // Do not get tips / do not sent transactions
)

var workersCount = 20 // Ammount of parallel workers for GetTransactionsToApprove and BroadcastTransaction

var ManualNodeList = []string{
	"http://my-own-nodes1:14265/",
	"http://my-own-nodes2:14265/",
	"http://my-own-nodes3:14265/",
}

// Do not change!
var attemptCount uint32
var attachedCount uint32
var finishedGoFuncCount int
var previousBranch giota.Trytes
var isFirstTTA bool = true
var firstTTA *giota.GetTransactionsToApproveResponse
var timeStart time.Time

type PowJob struct {
	Bundle  []giota.Transaction
	Trunk   giota.Trytes
	Branch  giota.Trytes
	MWM     int
	PowFunc giota.PowFunc
}

func getTransactionsToApprove(api *giota.API, channel chan *giota.GetTransactionsToApproveResponse) {
	var err error
	for {
		if attemptCount >= uint32(SpamCount) {
			finishedGoFuncCount++
			if finishedGoFuncCount >= workersCount {
				close(channel)
			}
			return
		}

		attemptCount++
		if !PowTest {
			transactionsToApprove, err := api.GetTransactionsToApprove(Depth, 0, "")
			if err != nil {
				logs.Log.Errorf("GetTransactionsToApprove: %v", err.Error())
				continue
			}
			channel <- transactionsToApprove
		} else {
			if isFirstTTA {
				firstTTA, err = api.GetTransactionsToApprove(Depth, 0, "")
				if err != nil {
					logs.Log.Errorf("GetTransactionsToApprove: %v", err.Error())
					continue
				}
				isFirstTTA = false
			}
			channel <- firstTTA
		}
	}
}

func doLocalPow(trytes []giota.Transaction, trunkTransaction giota.Trytes, branchTransaction giota.Trytes, mwm int, pow giota.PowFunc) error {
	var prev giota.Trytes
	var err error

	for i := len(trytes) - 1; i >= 0; i-- {
		switch {
		case i == len(trytes)-1:
			trytes[i].TrunkTransaction = trunkTransaction
			trytes[i].BranchTransaction = branchTransaction
		default:
			trytes[i].TrunkTransaction = prev
			trytes[i].BranchTransaction = trunkTransaction
		}

		timestamp := giota.Int2Trits(time.Now().UnixNano()/1000000, giota.TimestampTrinarySize).Trytes()
		trytes[i].AttachmentTimestamp = timestamp
		trytes[i].AttachmentTimestampLowerBound = giota.Int2Trits(0, giota.TimestampTrinarySize).Trytes()
		trytes[i].AttachmentTimestampUpperBound = "MMMMMMMMM" // (3^27-1)/2

		trytes[i].Nonce, err = pow(trytes[i].Trytes(), int(mwm))
		if err != nil {
			return err
		}

		prev = trytes[i].Hash()
	}
	return nil
}

func doPow(sendChannel chan []giota.Transaction, powChannel chan *PowJob) {
	for powJob := range powChannel {
		err := doLocalPow(powJob.Bundle, powJob.Trunk, powJob.Branch, powJob.MWM, powJob.PowFunc)
		if err != nil {
			logs.Log.Errorf("doLocalPow: %v", err.Error())
			continue
		}
		sendChannel <- []giota.Transaction(powJob.Bundle)
	}
}

func sendTransaction(api *giota.API, channel chan []giota.Transaction) {
	for attachedTransaction := range channel {
		if !PowTest {
			err := api.StoreTransactions(attachedTransaction)
			if err != nil {
				logs.Log.Errorf("StoreTransactions: %v", err.Error())
				continue
			}

			err = api.BroadcastTransactions(attachedTransaction)
			if err != nil {
				logs.Log.Errorf("BroadcastTransactions: %v", err.Error())
				continue
			}
		}
		attachedCount++
		avgTps := float32(attachedCount) / float32(time.Since(timeStart)/time.Second)
		logs.Log.Infof("[%d/%d] attached! avg TPS: %0.2f", attachedCount, SpamCount, avgTps)
	}
}

func main() {
	logs.Setup()
	logs.SetLogLevel("DEBUG")

	nodeClient := &http.Client{Timeout: 10 * time.Second} // Client to check IRI Healh at the beginning (smaller timeout)
	client := http.Client{Timeout: 120 * time.Second}     // Client for IRI API Requests
	apiPOW := giota.NewAPI(HostPOW, &client)

	var powFunc giota.PowFunc

	var apiNodeList []*giota.API
	var nodeList []string
	var err error

	if UseRemoteNodeList {
		nodeList, err = utils.ReadNodeList(NodeListURL)
		if err != nil {
			logs.Log.Fatalf("readNodeList: %v", err.Error())
		}
	} else {
		nodeList = ManualNodeList
	}

	logs.Log.Info("Looking for healthy nodes...")

	for _, node := range nodeList {
		apiNode := giota.NewAPI(node, nodeClient)
		resp, err := apiNode.GetNodeInfo()
		if err != nil {
			logs.Log.Warningf("   %v is NOT healthy. GetNodeInfo failed! Err: %v\n", node, err.Error())
			continue
		}
		if CheckNodeHealth {
			if (strings.HasPrefix(resp.AppVersion, "1.5.") || strings.HasPrefix(resp.AppVersion, "0.1.")) && ((resp.LatestMilestoneIndex - resp.LatestSolidSubtangleMilestoneIndex) < 2) {
				apiNodeList = append(apiNodeList, apiNode)
				logs.Log.Infof("   %v is healthy [v%v, %d/%d]\n", node, resp.AppVersion, resp.LatestSolidSubtangleMilestoneIndex, resp.LatestMilestoneIndex)
			} else {
				logs.Log.Warningf("   %v is NOT healthy [v%v, %d/%d]\n", node, resp.AppVersion, resp.LatestSolidSubtangleMilestoneIndex, resp.LatestMilestoneIndex)
			}
		} else {
			apiNodeList = append(apiNodeList, apiNode)
		}
	}

	logs.Log.Info("Looking for healthy nodes finished!")
	if len(apiNodeList) < 1 {
		logs.Log.Fatal("No healthy nodes found")
	}

	// At least one worker per node
	if workersCount < len(apiNodeList) {
		workersCount = len(apiNodeList)
	}

	message := utils.FromString(Message)
	tag, err := giota.ToTrytes(Tag)
	if err != nil {
		logs.Log.Fatalf("ToTrytes: %v", err.Error())
	}

	if UsePowSrv {
		powClient := &powsrv.PowClient{Network: "unix", Address: PowSrvPath, WriteTimeOutMs: 500, ReadTimeOutMs: 5000}
		powClient.Init()
		powFunc = powClient.PowFunc
	}

	channelGT := make(chan *giota.GetTransactionsToApproveResponse, workersCount) // Channel GetTransactionsToApprove
	channelST := make(chan []giota.Transaction, workersCount)                     // Channel SendTransactions

	for i := 0; i < workersCount; i++ {
		nodeListIndex := i % len(apiNodeList)
		go getTransactionsToApprove(apiNodeList[nodeListIndex], channelGT)
		go sendTransaction(apiNodeList[nodeListIndex], channelST)
	}

	channelPow := make(chan *PowJob, ParallelLevel) // Channel PowJobs
	for i := 0; i < ParallelLevel; i++ {
		go doPow(channelST, channelPow)
	}

	timeStart = time.Now()
	for transactionsToApprove := range channelGT {
		transfers := []giota.Transfer{
			giota.Transfer{
				Address: Address,
				Value:   0,
				Tag:     tag,
				Message: message,
			},
		}

		bundle, err := giota.PrepareTransfers(nil, "", transfers, nil, "", 2)
		if err != nil {
			logs.Log.Fatalf("PrepareTransfers: %v", err.Error())
		}

		if ReferenceLastTips && (previousBranch != "") {
			transactionsToApprove.TrunkTransaction = previousBranch
		}
		previousBranch = transactionsToApprove.BranchTransaction

		if UsePowSrv {
			powJob := PowJob{[]giota.Transaction(bundle), transactionsToApprove.TrunkTransaction, transactionsToApprove.BranchTransaction, MWM, powFunc}
			channelPow <- &powJob
		} else {
			attachToTangleRequest := giota.AttachToTangleRequest{
				TrunkTransaction:   transactionsToApprove.TrunkTransaction,
				BranchTransaction:  transactionsToApprove.BranchTransaction,
				MinWeightMagnitude: MWM,
				Trytes:             []giota.Transaction(bundle),
			}

			attached, err := apiPOW.AttachToTangle(&attachToTangleRequest)
			if err != nil {
				logs.Log.Errorf("AttachToTangle: %v", err.Error())
				continue
			}
			channelST <- attached.Trytes
		}
	}

	close(channelST)
}
