/*
Copyright IBM Corp. 2016 All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package endorser

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/hyperledger/fabric/common/channelconfig"
	"github.com/hyperledger/fabric/common/crypto"
	"github.com/hyperledger/fabric/common/flogging"
	"github.com/hyperledger/fabric/common/metrics"
	"github.com/hyperledger/fabric/common/util"
	"github.com/hyperledger/fabric/core/chaincode/platforms"
	"github.com/hyperledger/fabric/core/chaincode/shim"
	"github.com/hyperledger/fabric/core/common/ccprovider"
	"github.com/hyperledger/fabric/core/common/validation"
	"github.com/hyperledger/fabric/core/ledger"
	"github.com/hyperledger/fabric/protos/common"
	pb "github.com/hyperledger/fabric/protos/peer"
	"github.com/hyperledger/fabric/protos/transientstore"
	putils "github.com/hyperledger/fabric/protos/utils"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

var endorserLogger = flogging.MustGetLogger("endorser")

// The Jira issue that documents Endorser flow along with its relationship to
// the lifecycle chaincode - https://jira.hyperledger.org/browse/FAB-181

type privateDataDistributor func(channel string, txID string, privateData *transientstore.TxPvtReadWriteSetWithConfigInfo, blkHt uint64) error

// Support contains functions that the endorser requires to execute its tasks
type Support interface {
	crypto.SignerSupport
	// IsSysCCAndNotInvokableExternal returns true if the supplied chaincode is
	// a system chaincode and it NOT invokable
	IsSysCCAndNotInvokableExternal(name string) bool

	// GetTxSimulator returns the transaction simulator for the specified ledger
	// a client may obtain more than one such simulator; they are made unique
	// by way of the supplied txid
	GetTxSimulator(ledgername string, txid string) (ledger.TxSimulator, error)

	// GetHistoryQueryExecutor gives handle to a history query executor for the
	// specified ledger
	GetHistoryQueryExecutor(ledgername string) (ledger.HistoryQueryExecutor, error)

	// GetTransactionByID retrieves a transaction by id
	GetTransactionByID(chid, txID string) (*pb.ProcessedTransaction, error)

	// IsSysCC returns true if the name matches a system chaincode's
	// system chaincode names are system, chain wide
	IsSysCC(name string) bool

	// Execute - execute proposal, return original response of chaincode
	Execute(txParams *ccprovider.TransactionParams, cid, name, version, txid string, signedProp *pb.SignedProposal, prop *pb.Proposal, input *pb.ChaincodeInput) (*pb.Response, *pb.ChaincodeEvent, error)

	// ExecuteLegacyInit - executes a deployment proposal, return original response of chaincode
	ExecuteLegacyInit(txParams *ccprovider.TransactionParams, cid, name, version, txid string, signedProp *pb.SignedProposal, prop *pb.Proposal, spec *pb.ChaincodeDeploymentSpec) (*pb.Response, *pb.ChaincodeEvent, error)

	// GetChaincodeDefinition returns ccprovider.ChaincodeDefinition for the chaincode with the supplied name
	GetChaincodeDefinition(chaincodeID string, txsim ledger.QueryExecutor) (ccprovider.ChaincodeDefinition, error)

	// CheckACL checks the ACL for the resource for the channel using the
	// SignedProposal from which an id can be extracted for testing against a policy
	CheckACL(signedProp *pb.SignedProposal, chdr *common.ChannelHeader, shdr *common.SignatureHeader, hdrext *pb.ChaincodeHeaderExtension) error

	// IsJavaCC returns true if the CDS package bytes describe a chaincode
	// that requires the java runtime environment to execute
	IsJavaCC(buf []byte) (bool, error)

	// CheckInstantiationPolicy returns an error if the instantiation in the supplied
	// ChaincodeDefinition differs from the instantiation policy stored on the ledger
	CheckInstantiationPolicy(name, version string, cd ccprovider.ChaincodeDefinition) error

	// GetChaincodeDeploymentSpecFS returns the deploymentspec for a chaincode from the fs
	GetChaincodeDeploymentSpecFS(cds *pb.ChaincodeDeploymentSpec) (*pb.ChaincodeDeploymentSpec, error)

	// GetApplicationConfig returns the configtxapplication.SharedConfig for the Channel
	// and whether the Application config exists
	GetApplicationConfig(cid string) (channelconfig.Application, bool)

	// NewQueryCreator creates a new QueryCreator
	NewQueryCreator(channel string) (QueryCreator, error)

	// EndorseWithPlugin endorses the response with a plugin
	EndorseWithPlugin(ctx Context) (*pb.ProposalResponse, error)

	// GetLedgerHeight returns ledger height for given channelID
	GetLedgerHeight(channelID string) (uint64, error)
}

// Endorser provides the Endorser service ProcessProposal
type Endorser struct {
	distributePrivateData privateDataDistributor
	s                     Support
	PlatformRegistry      *platforms.Registry
	PvtRWSetAssembler
	Metrics *EndorserMetrics
}

// validateResult provides the result of endorseProposal verification
type validateResult struct {
	prop    *pb.Proposal
	hdrExt  *pb.ChaincodeHeaderExtension
	chainID string
	txid    string
	resp    *pb.ProposalResponse
}

// NewEndorserServer creates and returns a new Endorser server instance.
func NewEndorserServer(privDist privateDataDistributor, s Support, pr *platforms.Registry, metricsProv metrics.Provider) *Endorser {
	e := &Endorser{
		distributePrivateData: privDist,
		s:                     s,
		PlatformRegistry:      pr,
		PvtRWSetAssembler:     &rwSetAssembler{},
		Metrics:               NewEndorserMetrics(metricsProv),
	}
	return e
}

// call specified chaincode (system or user)
func (e *Endorser) callChaincode(txParams *ccprovider.TransactionParams, version string, input *pb.ChaincodeInput, cid *pb.ChaincodeID) (*pb.Response, *pb.ChaincodeEvent, error) {
	endorserLogger.Infof("[%s][%s] Entry chaincode: %s", txParams.ChannelID, shorttxid(txParams.TxID), cid)
	defer func(start time.Time) {
		logger := endorserLogger.WithOptions(zap.AddCallerSkip(1))
		elapsedMilliseconds := time.Since(start).Round(time.Millisecond) / time.Millisecond
		logger.Infof("[%s][%s] Exit chaincode: %s (%dms)", txParams.ChannelID, shorttxid(txParams.TxID), cid, elapsedMilliseconds)
	}(time.Now())

	var err error
	var res *pb.Response
	var ccevent *pb.ChaincodeEvent

	// is this a system chaincode
	// 执行链码 返回响应结果和事件
	// 实现： fabric\core\endorser\support.go No.135 line
	res, ccevent, err = e.s.Execute(txParams, txParams.ChannelID, cid.Name, version, txParams.TxID, txParams.SignedProp, txParams.Proposal, input)
	if err != nil {
		return nil, nil, err
	}

	// per doc anything < 400 can be sent as TX.
	// fabric errors will always be >= 400 (ie, unambiguous errors )
	// "lscc" will respond with status 200 or 500 (ie, unambiguous OK or ERROR)
	if res.Status >= shim.ERRORTHRESHOLD { //判断响应状态
		return res, nil, nil
	}

	// ----- BEGIN -  SECTION THAT MAY NEED TO BE DONE IN LSCC ------
	// if this a call to deploy a chaincode, We need a mechanism
	// to pass TxSimulator into LSCC. Till that is worked out this
	// special code does the actual deploy, upgrade here so as to collect
	// all state under one TxSimulator
	//
	// NOTE that if there's an error all simulation, including the chaincode
	// table changes in lscc will be thrown away
	// 判断调用链码是lscc 检测是否是实例化和升级 是执行下面操作 如果不是直接返回
	if cid.Name == "lscc" && len(input.Args) >= 3 && (string(input.Args[0]) == "deploy" || string(input.Args[0]) == "upgrade") {
		userCDS, err := putils.GetChaincodeDeploymentSpec(input.Args[2], e.PlatformRegistry) //获取链码规则 并根据编程语言（golang java nodejs）验证规则
		if err != nil {
			return nil, nil, err
		}

		var cds *pb.ChaincodeDeploymentSpec
		cds, err = e.SanitizeUserCDS(userCDS) //从安装链码路径获取链码规则
		if err != nil {
			return nil, nil, err
		}

		// this should not be a system chaincode
		if e.s.IsSysCC(cds.ChaincodeSpec.ChaincodeId.Name) { //必须不是系统链码 没有权限去实例化和升级系统链码
			return nil, nil, errors.Errorf("attempting to deploy a system chaincode %s/%s", cds.ChaincodeSpec.ChaincodeId.Name, txParams.ChannelID)
		}
		// 初始化链码 实例化和升级都需要执行一下链码里的 初始化init()函数
		_, _, err = e.s.ExecuteLegacyInit(txParams, txParams.ChannelID, cds.ChaincodeSpec.ChaincodeId.Name, cds.ChaincodeSpec.ChaincodeId.Version, txParams.TxID, txParams.SignedProp, txParams.Proposal, cds)
		if err != nil {
			// increment the failure to indicate instantion/upgrade failures
			meterLabels := []string{
				"channel", txParams.ChannelID,
				"chaincode", cds.ChaincodeSpec.ChaincodeId.Name + ":" + cds.ChaincodeSpec.ChaincodeId.Version,
			}
			e.Metrics.InitFailed.With(meterLabels...).Add(1)
			return nil, nil, err
		}
	}
	// ----- END -------

	return res, ccevent, err
}

// SanitizeUserCDS
func (e *Endorser) SanitizeUserCDS(userCDS *pb.ChaincodeDeploymentSpec) (*pb.ChaincodeDeploymentSpec, error) {
	fsCDS, err := e.s.GetChaincodeDeploymentSpecFS(userCDS) //检测链码是否安装 返回链码包 实现： fabric\core\endorser\support.go No.115 line
	if err != nil {
		return nil, errors.Wrapf(err, "cannot deploy a chaincode which is not installed")
	}

	sanitizedCDS := proto.Clone(fsCDS).(*pb.ChaincodeDeploymentSpec)
	sanitizedCDS.CodePackage = nil
	sanitizedCDS.ChaincodeSpec.Input = userCDS.ChaincodeSpec.Input

	return sanitizedCDS, nil
}

// SimulateProposal simulates the proposal by calling the chaincode
func (e *Endorser) SimulateProposal(txParams *ccprovider.TransactionParams, cid *pb.ChaincodeID) (ccprovider.ChaincodeDefinition, *pb.Response, []byte, *pb.ChaincodeEvent, error) {
	endorserLogger.Debugf("[%s][%s] Entry chaincode: %s", txParams.ChannelID, shorttxid(txParams.TxID), cid)
	defer endorserLogger.Debugf("[%s][%s] Exit", txParams.ChannelID, shorttxid(txParams.TxID))
	// we do expect the payload to be a ChaincodeInvocationSpec
	// if we are supporting other payloads in future, this be glaringly point
	// as something that should change
	cis, err := putils.GetChaincodeInvocationSpec(txParams.Proposal) //获取链码调用相关规则信息
	if err != nil {
		return nil, nil, nil, nil, err
	}

	var cdLedger ccprovider.ChaincodeDefinition
	var version string

	if !e.s.IsSysCC(cid.Name) { //判断链码名称是否是系统链码
		cdLedger, err = e.s.GetChaincodeDefinition(cid.Name, txParams.TXSimulator) //根据链码名称获取链码定义信息
		if err != nil {
			return nil, nil, nil, nil, errors.WithMessage(err, fmt.Sprintf("make sure the chaincode %s has been successfully instantiated and try again", cid.Name))
		}
		version = cdLedger.CCVersion() //获取链码版本 一般都是最后实例化版本  //实现 fabric\core\common\ccprovider\ccprovider.go No.463 line

		err = e.s.CheckInstantiationPolicy(cid.Name, version, cdLedger) //根据name/version组成key去缓存获取 检测获取链码策略与账本保存的实例化策略是否一致 实现： fabric\core\common\ccprovider\ccprovider.go No.254 line
		if err != nil {
			return nil, nil, nil, nil, err
		}
	} else {
		version = util.GetSysCCVersion()
	}

	// ---3. execute the proposal and get simulation results
	var simResult *ledger.TxSimulationResults
	var pubSimResBytes []byte
	var res *pb.Response
	var ccevent *pb.ChaincodeEvent
	res, ccevent, err = e.callChaincode(txParams, version, cis.ChaincodeSpec.Input, cid) //进行链码模拟执行
	if err != nil {
		endorserLogger.Errorf("[%s][%s] failed to invoke chaincode %s, error: %+v", txParams.ChannelID, shorttxid(txParams.TxID), cid, err)
		return nil, nil, nil, nil, err
	}

	if txParams.TXSimulator != nil {
		if simResult, err = txParams.TXSimulator.GetTxSimulationResults(); err != nil { //获得模拟执行结果集
			txParams.TXSimulator.Done()
			return nil, nil, nil, nil, err
		}

		if simResult.PvtSimulationResults != nil { // 判断结果集中是否存在私有读写集
			if cid.Name == "lscc" {
				// TODO: remove once we can store collection configuration outside of LSCC
				txParams.TXSimulator.Done()
				return nil, nil, nil, nil, errors.New("Private data is forbidden to be used in instantiate")
			}
			pvtDataWithConfig, err := e.AssemblePvtRWSet(simResult.PvtSimulationResults, txParams.TXSimulator)
			// To read collection config need to read collection updates before
			// releasing the lock, hence txParams.TXSimulator.Done()  moved down here
			txParams.TXSimulator.Done()

			if err != nil {
				return nil, nil, nil, nil, errors.WithMessage(err, "failed to obtain collections config")
			}
			endorsedAt, err := e.s.GetLedgerHeight(txParams.ChannelID) //根据通道获取区块高度
			if err != nil {
				return nil, nil, nil, nil, errors.WithMessage(err, fmt.Sprint("failed to obtain ledger height for channel", txParams.ChannelID))
			}
			// Add ledger height at which transaction was endorsed,
			// `endorsedAt` is obtained from the block storage and at times this could be 'endorsement Height + 1'.
			// However, since we use this height only to select the configuration (3rd parameter in distributePrivateData) and
			// manage transient store purge for orphaned private writesets (4th parameter in distributePrivateData), this works for now.
			// Ideally, ledger should add support in the simulator as a first class function `GetHeight()`.
			pvtDataWithConfig.EndorsedAt = endorsedAt
			if err := e.distributePrivateData(txParams.ChannelID, txParams.TxID, pvtDataWithConfig, endorsedAt); err != nil { //
				return nil, nil, nil, nil, err
			}
		}

		txParams.TXSimulator.Done()
		if pubSimResBytes, err = simResult.GetPubSimulationBytes(); err != nil { //共有数据结果集序列化
			return nil, nil, nil, nil, err
		}
	}
	return cdLedger, res, pubSimResBytes, ccevent, nil
}

// endorse the proposal by calling the ESCC
func (e *Endorser) endorseProposal(_ context.Context, chainID string, txid string, signedProp *pb.SignedProposal, proposal *pb.Proposal, response *pb.Response, simRes []byte, event *pb.ChaincodeEvent, visibility []byte, ccid *pb.ChaincodeID, txsim ledger.TxSimulator, cd ccprovider.ChaincodeDefinition) (*pb.ProposalResponse, error) {
	endorserLogger.Debugf("[%s][%s] Entry chaincode: %s", chainID, shorttxid(txid), ccid)
	defer endorserLogger.Debugf("[%s][%s] Exit", chainID, shorttxid(txid))

	isSysCC := cd == nil
	// 1) extract the name of the escc that is requested to endorse this chaincode
	var escc string
	// ie, "lscc" or system chaincodes
	if isSysCC {
		escc = "escc"
	} else {
		escc = cd.Endorsement() //通常是escc
	}

	endorserLogger.Debugf("[%s][%s] escc for chaincode %s is %s", chainID, shorttxid(txid), ccid, escc)

	// marshalling event bytes
	var err error
	var eventBytes []byte
	if event != nil {
		eventBytes, err = putils.GetBytesChaincodeEvent(event) //获取链码事件 序列化字节
		if err != nil {
			return nil, errors.Wrap(err, "failed to marshal event bytes")
		}
	}

	// set version of executing chaincode
	if isSysCC {
		// if we want to allow mixed fabric levels we should
		// set syscc version to ""
		ccid.Version = util.GetSysCCVersion()
	} else {
		ccid.Version = cd.CCVersion() //获取链码版本
	}

	ctx := Context{
		PluginName:     escc,
		Channel:        chainID,
		SignedProposal: signedProp,
		ChaincodeID:    ccid,
		Event:          eventBytes,
		SimRes:         simRes,
		Response:       response,
		Visibility:     visibility,
		Proposal:       proposal,
		TxID:           txid,
	}
	return e.s.EndorseWithPlugin(ctx) //背书 实现： fabric\core\endorser\plugin_endorser.go No.162 line
}

// preProcess checks the tx proposal headers, uniqueness and ACL
func (e *Endorser) preProcess(signedProp *pb.SignedProposal) (*validateResult, error) {
	vr := &validateResult{}
	// at first, we check whether the message is valid
	prop, hdr, hdrExt, err := validation.ValidateProposalMessage(signedProp) //验证签署的提案 包含 头部信息 验证身份 等

	if err != nil {
		e.Metrics.ProposalValidationFailed.Add(1)
		vr.resp = &pb.ProposalResponse{Response: &pb.Response{Status: 500, Message: err.Error()}}
		return vr, err
	}

	chdr, err := putils.UnmarshalChannelHeader(hdr.ChannelHeader) // 通道header信息
	if err != nil {
		vr.resp = &pb.ProposalResponse{Response: &pb.Response{Status: 500, Message: err.Error()}}
		return vr, err
	}

	shdr, err := putils.GetSignatureHeader(hdr.SignatureHeader) // 签署header信息
	if err != nil {
		vr.resp = &pb.ProposalResponse{Response: &pb.Response{Status: 500, Message: err.Error()}}
		return vr, err
	}

	// block invocations to security-sensitive system chaincodes
	// 检测提案中链码名称 是否是系统链码名称并且是外部不可调用的链码
	if e.s.IsSysCCAndNotInvokableExternal(hdrExt.ChaincodeId.Name) { //实现方法 ：fabric\core\endorser\support.go No.55 line
		endorserLogger.Errorf("Error: an attempt was made by %#v to invoke system chaincode %s", shdr.Creator, hdrExt.ChaincodeId.Name)
		err = errors.Errorf("chaincode %s cannot be invoked through a proposal", hdrExt.ChaincodeId.Name)
		vr.resp = &pb.ProposalResponse{Response: &pb.Response{Status: 500, Message: err.Error()}}
		return vr, err
	}

	chainID := chdr.ChannelId
	txid := chdr.TxId
	endorserLogger.Debugf("[%s][%s] processing txid: %s", chainID, shorttxid(txid), txid)

	if chainID != "" {
		// labels that provide context for failure metrics
		meterLabels := []string{
			"channel", chainID,
			"chaincode", hdrExt.ChaincodeId.Name + ":" + hdrExt.ChaincodeId.Version,
		}

		// Here we handle uniqueness check and ACLs for proposals targeting a chain
		// Notice that ValidateProposalMessage has already verified that TxID is computed properly
		// 判断此交易txid是否已经在账本存在过
		if _, err = e.s.GetTransactionByID(chainID, txid); err == nil { //实现方法：fabric\core\endorser\support.go No.81 line
			// increment failure due to duplicate transactions. Useful for catching replay attacks in
			// addition to benign retries
			e.Metrics.DuplicateTxsFailure.With(meterLabels...).Add(1)
			err = errors.Errorf("duplicate transaction found [%s]. Creator [%x]", txid, shdr.Creator)
			vr.resp = &pb.ProposalResponse{Response: &pb.Response{Status: 500, Message: err.Error()}}
			return vr, err
		}

		// check ACL only for application chaincodes; ACLs
		// for system chaincodes are checked elsewhere
		if !e.s.IsSysCC(hdrExt.ChaincodeId.Name) { //检测是否是系统合约 实现方法 fabric\core\endorser\support.go No.110 line
			// check that the proposal complies with the Channel's writers
			if err = e.s.CheckACL(signedProp, chdr, shdr, hdrExt); err != nil { //检测是否满足ACL权限  实现方法：fabric\core\endorser\support.go No.157 line
				e.Metrics.ProposalACLCheckFailed.With(meterLabels...).Add(1)
				vr.resp = &pb.ProposalResponse{Response: &pb.Response{Status: 500, Message: err.Error()}}
				return vr, err
			}
		}
	} else {
		// chainless proposals do not/cannot affect ledger and cannot be submitted as transactions
		// ignore uniqueness checks; also, chainless proposals are not validated using the policies
		// of the chain since by definition there is no chain; they are validated against the local
		// MSP of the peer instead by the call to ValidateProposalMessage above
	}

	vr.prop, vr.hdrExt, vr.chainID, vr.txid = prop, hdrExt, chainID, txid
	return vr, nil
}

// ProcessProposal process the Proposal
func (e *Endorser) ProcessProposal(ctx context.Context, signedProp *pb.SignedProposal) (*pb.ProposalResponse, error) {
	// start time for computing elapsed time metric for successfully endorsed proposals
	startTime := time.Now()            //节点处理提案开始时间
	e.Metrics.ProposalsReceived.Add(1) //节点收到提案数+1 与最后成功背书提案+1 计数项

	addr := util.ExtractRemoteAddress(ctx) //获取发起提案节点地址
	endorserLogger.Debug("Entering: request from", addr)

	// variables to capture proposal duration metric
	var chainID string
	var hdrExt *pb.ChaincodeHeaderExtension
	var success bool
	defer func() { //函数结束时调用
		// capture proposal duration metric. hdrExt == nil indicates early failure
		// where we don't capture latency metric. But the ProposalValidationFailed
		// counter metric should shed light on those failures.
		if hdrExt != nil {
			meterLabels := []string{
				"channel", chainID,
				"chaincode", hdrExt.ChaincodeId.Name + ":" + hdrExt.ChaincodeId.Version,
				"success", strconv.FormatBool(success),
			}
			e.Metrics.ProposalDuration.With(meterLabels...).Observe(time.Since(startTime).Seconds())
		}

		endorserLogger.Debug("Exit: request from", addr)
	}()

	// 0 -- check and validate
	vr, err := e.preProcess(signedProp) //对提案进行预处理  如果比较抽象的化 可以解析一个区块转成json 对比进行学习会更好
	if err != nil {
		resp := vr.resp
		return resp, err
	}

	prop, hdrExt, chainID, txid := vr.prop, vr.hdrExt, vr.chainID, vr.txid

	// obtaining once the tx simulator for this proposal. This will be nil
	// for chainless proposals
	// Also obtain a history query executor for history queries, since tx simulator does not cover history
	var txsim ledger.TxSimulator                            //定义了一个Tx模拟器，用于后面的模拟交易过程,如果通道Id为空，那么TxSimulator也是空
	var historyQueryExecutor ledger.HistoryQueryExecutor    //定义一个历史查询器
	if acquireTxSimulator(chainID, vr.hdrExt.ChaincodeId) { //是否需要tx模拟  基本qscc(查询系统链码)和cscc(配置系统链码)是不需要交易模拟器
		if txsim, err = e.s.GetTxSimulator(chainID, txid); err != nil { //创建新的tx模拟器 实现方法 fabric\core\endorser\support.go No.62 line
			return &pb.ProposalResponse{Response: &pb.Response{Status: 500, Message: err.Error()}}, nil
		}

		// txsim acquires a shared lock on the stateDB. As this would impact the block commits (i.e., commit
		// of valid write-sets to the stateDB), we must release the lock as early as possible.
		// Hence, this txsim object is closed in simulateProposal() as soon as the tx is simulated and
		// rwset is collected before gossip dissemination if required for privateData. For safety, we
		// add the following defer statement and is useful when an error occur. Note that calling
		// txsim.Done() more than once does not cause any issue. If the txsim is already
		// released, the following txsim.Done() simply returns.
		defer txsim.Done()

		if historyQueryExecutor, err = e.s.GetHistoryQueryExecutor(chainID); err != nil { //历史查询器 实现方法 fabric\core\endorser\support.go No.72 line
			return &pb.ProposalResponse{Response: &pb.Response{Status: 500, Message: err.Error()}}, nil
		}
	}

	txParams := &ccprovider.TransactionParams{
		ChannelID:            chainID,
		TxID:                 txid,
		SignedProp:           signedProp,
		Proposal:             prop,
		TXSimulator:          txsim,
		HistoryQueryExecutor: historyQueryExecutor,
	}
	// this could be a request to a chainless SysCC

	// TODO: if the proposal has an extension, it will be of type ChaincodeAction;
	//       if it's present it means that no simulation is to be performed because
	//       we're trying to emulate a submitting peer. On the other hand, we need
	//       to validate the supplied action before endorsing it

	// 1 -- simulate
	cd, res, simulationResult, ccevent, err := e.SimulateProposal(txParams, hdrExt.ChaincodeId) //模拟交易执行 返回链码信息，响应结果，模拟执行结果读写集，链码事件
	if err != nil {
		return &pb.ProposalResponse{Response: &pb.Response{Status: 500, Message: err.Error()}}, nil
	}
	if res != nil {
		if res.Status >= shim.ERROR { //响应失败
			endorserLogger.Errorf("[%s][%s] simulateProposal() resulted in chaincode %s response status %d for txid: %s", chainID, shorttxid(txid), hdrExt.ChaincodeId, res.Status, txid)
			var cceventBytes []byte
			if ccevent != nil {
				cceventBytes, err = putils.GetBytesChaincodeEvent(ccevent) //序列化链码事件
				if err != nil {
					return nil, errors.Wrap(err, "failed to marshal event bytes")
				}
			}
			pResp, err := putils.CreateProposalResponseFailure(prop.Header, prop.Payload, res, simulationResult, cceventBytes, hdrExt.ChaincodeId, hdrExt.PayloadVisibility)
			if err != nil {
				return &pb.ProposalResponse{Response: &pb.Response{Status: 500, Message: err.Error()}}, nil
			}

			return pResp, nil
		}
	}

	// 2 -- endorse and get a marshalled ProposalResponse message
	var pResp *pb.ProposalResponse

	// TODO till we implement global ESCC, CSCC for system chaincodes
	// chainless proposals (such as CSCC) don't have to be endorsed
	if chainID == "" {
		pResp = &pb.ProposalResponse{Response: res}
	} else {
		// Note: To endorseProposal(), we pass the released txsim. Hence, an error would occur if we try to use this txsim
		pResp, err = e.endorseProposal(ctx, chainID, txid, signedProp, prop, res, simulationResult, ccevent, hdrExt.PayloadVisibility, hdrExt.ChaincodeId, txsim, cd) //进行背书操作

		// if error, capture endorsement failure metric
		meterLabels := []string{
			"channel", chainID,
			"chaincode", hdrExt.ChaincodeId.Name + ":" + hdrExt.ChaincodeId.Version,
		}

		if err != nil {
			meterLabels = append(meterLabels, "chaincodeerror", strconv.FormatBool(false))
			e.Metrics.EndorsementsFailed.With(meterLabels...).Add(1)
			return &pb.ProposalResponse{Response: &pb.Response{Status: 500, Message: err.Error()}}, nil
		}
		if pResp.Response.Status >= shim.ERRORTHRESHOLD {
			// the default ESCC treats all status codes about threshold as errors and fails endorsement
			// useful to track this as a separate metric
			meterLabels = append(meterLabels, "chaincodeerror", strconv.FormatBool(true))
			e.Metrics.EndorsementsFailed.With(meterLabels...).Add(1)
			endorserLogger.Debugf("[%s][%s] endorseProposal() resulted in chaincode %s error for txid: %s", chainID, shorttxid(txid), hdrExt.ChaincodeId, txid)
			return pResp, nil
		}
	}

	// Set the proposal response payload - it
	// contains the "return value" from the
	// chaincode invocation
	pResp.Response = res

	// total failed proposals = ProposalsReceived-SuccessfulProposals
	e.Metrics.SuccessfulProposals.Add(1)
	success = true

	return pResp, nil
}

// determine whether or not a transaction simulator should be
// obtained for a proposal.
func acquireTxSimulator(chainID string, ccid *pb.ChaincodeID) bool {
	if chainID == "" {
		return false
	}

	// ¯\_(ツ)_/¯ locking.
	// Don't get a simulator for the query and config system chaincode.
	// These don't need the simulator and its read lock results in deadlocks.
	switch ccid.Name {
	case "qscc", "cscc":
		return false
	default:
		return true
	}
}

// shorttxid replicates the chaincode package function to shorten txids.
// ~~TODO utilize a common shorttxid utility across packages.~~
// TODO use a formal type for transaction ID and make it a stringer
func shorttxid(txid string) string {
	if len(txid) < 8 {
		return txid
	}
	return txid[0:8]
}
