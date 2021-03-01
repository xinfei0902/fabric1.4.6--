/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package common

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/hyperledger/fabric/bccsp/factory"
	"github.com/hyperledger/fabric/common/channelconfig"
	"github.com/hyperledger/fabric/common/flogging"
	"github.com/hyperledger/fabric/common/viperutil"
	"github.com/hyperledger/fabric/core/comm"
	"github.com/hyperledger/fabric/core/config"
	"github.com/hyperledger/fabric/core/scc/cscc"
	"github.com/hyperledger/fabric/msp"
	mspmgmt "github.com/hyperledger/fabric/msp/mgmt"
	"github.com/hyperledger/fabric/peer/common/api"
	pcommon "github.com/hyperledger/fabric/protos/common"
	pb "github.com/hyperledger/fabric/protos/peer"
	putils "github.com/hyperledger/fabric/protos/utils"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// UndefinedParamValue defines what undefined parameters in the command line will initialise to
const UndefinedParamValue = ""
const CmdRoot = "core"

var mainLogger = flogging.MustGetLogger("main")
var logOutput = os.Stderr

var ( //与下面init()函数对应 可追踪
	defaultConnTimeout = 3 * time.Second
	// These function variables (xyzFnc) can be used to invoke corresponding xyz function
	// this will allow the invoking packages to mock these functions in their unit test cases

	// GetEndorserClientFnc is a function that returns a new endorser client connection
	// to the provided peer address using the TLS root cert file,
	// by default it is set to GetEndorserClient function
	GetEndorserClientFnc func(address, tlsRootCertFile string) (pb.EndorserClient, error)

	// GetPeerDeliverClientFnc is a function that returns a new deliver client connection
	// to the provided peer address using the TLS root cert file,
	// by default it is set to GetDeliverClient function
	GetPeerDeliverClientFnc func(address, tlsRootCertFile string) (api.PeerDeliverClient, error)

	// GetDeliverClientFnc is a function that returns a new deliver client connection
	// to the provided peer address using the TLS root cert file,
	// by default it is set to GetDeliverClient function
	GetDeliverClientFnc func(address, tlsRootCertFile string) (pb.Deliver_DeliverClient, error)

	// GetDefaultSignerFnc is a function that returns a default Signer(Default/PERR)
	// by default it is set to GetDefaultSigner function
	GetDefaultSignerFnc func() (msp.SigningIdentity, error)

	// GetBroadcastClientFnc returns an instance of the BroadcastClient interface
	// by default it is set to GetBroadcastClient function
	GetBroadcastClientFnc func() (BroadcastClient, error)

	// GetOrdererEndpointOfChainFnc returns orderer endpoints of given chain
	// by default it is set to GetOrdererEndpointOfChain function
	GetOrdererEndpointOfChainFnc func(chainID string, signer msp.SigningIdentity,
		endorserClient pb.EndorserClient) ([]string, error)

	// GetCertificateFnc is a function that returns the client TLS certificate
	GetCertificateFnc func() (tls.Certificate, error)
)

type commonClient struct {
	*comm.GRPCClient
	address string
	sn      string
}

func init() {
	GetEndorserClientFnc = GetEndorserClient
	GetDefaultSignerFnc = GetDefaultSigner
	GetBroadcastClientFnc = GetBroadcastClient
	GetOrdererEndpointOfChainFnc = GetOrdererEndpointOfChain
	GetDeliverClientFnc = GetDeliverClient
	GetPeerDeliverClientFnc = GetPeerDeliverClient
	GetCertificateFnc = GetCertificate
}

// InitConfig initializes viper config
func InitConfig(cmdRoot string) error {

	err := config.InitViper(nil, cmdRoot) //把配置文件路径已经 前缀名称复制给viper中
	if err != nil {
		return err
	}

	err = viper.ReadInConfig() // Find and read the config file
	if err != nil {            // Handle errors reading the config file
		// The version of Viper we use claims the config type isn't supported when in fact the file hasn't been found
		// Display a more helpful message to avoid confusing the user.
		if strings.Contains(fmt.Sprint(err), "Unsupported Config Type") {
			return errors.New(fmt.Sprintf("Could not find config file. "+
				"Please make sure that FABRIC_CFG_PATH is set to a path "+
				"which contains %s.yaml", cmdRoot))
		} else {
			return errors.WithMessage(err, fmt.Sprintf("error when reading %s config file", cmdRoot))
		}
	}

	return nil
}

// InitCrypto initializes crypto for this peer
func InitCrypto(mspMgrConfigDir, localMSPID, localMSPType string) error {
	var err error
	// Check whether msp folder exists
	fi, err := os.Stat(mspMgrConfigDir)
	if os.IsNotExist(err) || !fi.IsDir() {
		// No need to try to load MSP from folder which is not available
		return errors.Errorf("cannot init crypto, folder \"%s\" does not exist", mspMgrConfigDir)
	}
	// Check whether localMSPID exists
	if localMSPID == "" {
		return errors.New("the local MSP must have an ID")
	}

	// Init the BCCSP
	SetBCCSPKeystorePath() //设置bccsp中keystore为key keystore路径为v 存放viper
	var bccspConfig *factory.FactoryOpts
	err = viperutil.EnhancedExactUnmarshalKey("peer.BCCSP", &bccspConfig) //将peer中core.yaml的 peer: Bccsp：中子树列 k v写入到bccspConfig结构中
	if err != nil {
		return errors.WithMessage(err, "could not parse YAML config")
	}

	err = mspmgmt.LoadLocalMspWithType(mspMgrConfigDir, bccspConfig, localMSPID, localMSPType) //加载 根据mspType(默认bsscp)获取指定路径msp到mspmgmt（msp管理）
	if err != nil {
		return errors.WithMessage(err, fmt.Sprintf("error when setting up MSP of type %s from directory %s", localMSPType, mspMgrConfigDir))
	}

	return nil
}

// SetBCCSPKeystorePath sets the file keystore path for the SW BCCSP provider
// to an absolute path relative to the config file
func SetBCCSPKeystorePath() {
	viper.Set("peer.BCCSP.SW.FileKeyStore.KeyStore",
		config.GetPath("peer.BCCSP.SW.FileKeyStore.KeyStore"))
}

// GetDefaultSigner return a default Signer(Default/PERR) for cli
func GetDefaultSigner() (msp.SigningIdentity, error) {
	signer, err := mspmgmt.GetLocalMSP().GetDefaultSigningIdentity() //获取签署身份 实现：fabric\msp\mspimpl.go No.269 line
	if err != nil {
		return nil, errors.WithMessage(err, "error obtaining the default signing identity")
	}

	return signer, err
}

// GetOrdererEndpointOfChain returns orderer endpoints of given chain
func GetOrdererEndpointOfChain(chainID string, signer msp.SigningIdentity, endorserClient pb.EndorserClient) ([]string, error) {
	// query cscc for chain config block
	invocation := &pb.ChaincodeInvocationSpec{
		ChaincodeSpec: &pb.ChaincodeSpec{
			Type:        pb.ChaincodeSpec_Type(pb.ChaincodeSpec_Type_value["GOLANG"]),
			ChaincodeId: &pb.ChaincodeID{Name: "cscc"},
			Input:       &pb.ChaincodeInput{Args: [][]byte{[]byte(cscc.GetConfigBlock), []byte(chainID)}},
		},
	}

	creator, err := signer.Serialize() //签署者身份 MSPID pem   实现： github.com\hyperledger\fabric\msp\identities.go  No.174 line
	if err != nil {
		return nil, errors.WithMessage(err, fmt.Sprintf("error serializing identity for %s", signer.GetIdentifier()))
	}

	prop, _, err := putils.CreateProposalFromCIS(pcommon.HeaderType_CONFIG, "", invocation, creator) //构建提案
	if err != nil {
		return nil, errors.WithMessage(err, "error creating GetConfigBlock proposal")
	}

	signedProp, err := putils.GetSignedProposal(prop, signer) //对提案进行签名
	if err != nil {
		return nil, errors.WithMessage(err, "error creating signed GetConfigBlock proposal")
	}

	proposalResp, err := endorserClient.ProcessProposal(context.Background(), signedProp) //把已经签好的提案提交给背书节点 并返回模拟执行的结果
	if err != nil {
		return nil, errors.WithMessage(err, "error endorsing GetConfigBlock")
	}

	if proposalResp == nil {
		return nil, errors.WithMessage(err, "error nil proposal response")
	}

	if proposalResp.Response.Status != 0 && proposalResp.Response.Status != 200 {
		return nil, errors.Errorf("error bad proposal response %d: %s", proposalResp.Response.Status, proposalResp.Response.Message) //这几个错误也是比较常见的
	}

	// parse config block
	block, err := putils.GetBlockFromBlockBytes(proposalResp.Response.Payload) //从返回结果中获取信息转block 把结构和解析出的区块对比 就比较明显了
	if err != nil {
		return nil, errors.WithMessage(err, "error unmarshaling config block")
	}

	envelopeConfig, err := putils.ExtractEnvelope(block, 0) //对块中data数据进行信封封装 还记得新增组织中最后提交的是封装信封信息吗？
	if err != nil {
		return nil, errors.WithMessage(err, "error extracting config block envelope")
	}
	bundle, err := channelconfig.NewBundleFromEnvelope(envelopeConfig)
	if err != nil {
		return nil, errors.WithMessage(err, "error loading config block")
	}

	return bundle.ChannelConfig().OrdererAddresses(), nil //OrdererAddresses() 实现: fabric\common\channelconfig\channel.go No.152 line
}

// CheckLogLevel checks that a given log level string is valid
func CheckLogLevel(level string) error {
	if !flogging.IsValidLevel(level) {
		return errors.Errorf("invalid log level provided - %s", level)
	}
	return nil
}

func configFromEnv(prefix string) (address, override string, clientConfig comm.ClientConfig, err error) { //从环境变量中回去配置信息
	address = viper.GetString(prefix + ".address")
	override = viper.GetString(prefix + ".tls.serverhostoverride")
	clientConfig = comm.ClientConfig{}
	connTimeout := viper.GetDuration(prefix + ".client.connTimeout")
	if connTimeout == time.Duration(0) {
		connTimeout = defaultConnTimeout
	}
	clientConfig.Timeout = connTimeout
	secOpts := &comm.SecureOptions{
		UseTLS:            viper.GetBool(prefix + ".tls.enabled"),
		RequireClientCert: viper.GetBool(prefix + ".tls.clientAuthRequired")}
	if secOpts.UseTLS {
		caPEM, res := ioutil.ReadFile(config.GetPath(prefix + ".tls.rootcert.file"))
		if res != nil {
			err = errors.WithMessage(res,
				fmt.Sprintf("unable to load %s.tls.rootcert.file", prefix))
			return
		}
		secOpts.ServerRootCAs = [][]byte{caPEM}
	}
	if secOpts.RequireClientCert {
		keyPEM, res := ioutil.ReadFile(config.GetPath(prefix + ".tls.clientKey.file"))
		if res != nil {
			err = errors.WithMessage(res,
				fmt.Sprintf("unable to load %s.tls.clientKey.file", prefix))
			return
		}
		secOpts.Key = keyPEM
		certPEM, res := ioutil.ReadFile(config.GetPath(prefix + ".tls.clientCert.file"))
		if res != nil {
			err = errors.WithMessage(res,
				fmt.Sprintf("unable to load %s.tls.clientCert.file", prefix))
			return
		}
		secOpts.Certificate = certPEM
	}
	clientConfig.SecOpts = secOpts
	return
}

func InitCmd(cmd *cobra.Command, args []string) {
	err := InitConfig(CmdRoot) //初始化core.yaml配置文件
	if err != nil {            // Handle errors reading the config file
		mainLogger.Errorf("Fatal error when initializing %s config : %s", CmdRoot, err)
		os.Exit(1)
	}

	// read in the legacy logging level settings and, if set,
	// notify users of the FABRIC_LOGGING_SPEC env variable
	var loggingLevel string
	if viper.GetString("logging_level") != "" { //获取日志等级级别
		loggingLevel = viper.GetString("logging_level")
	} else {
		loggingLevel = viper.GetString("logging.level")
	}
	if loggingLevel != "" {
		mainLogger.Warning("CORE_LOGGING_LEVEL is no longer supported, please use the FABRIC_LOGGING_SPEC environment variable")
	}

	loggingSpec := os.Getenv("FABRIC_LOGGING_SPEC")
	loggingFormat := os.Getenv("FABRIC_LOGGING_FORMAT")

	flogging.Init(flogging.Config{
		Format:  loggingFormat,
		Writer:  logOutput,
		LogSpec: loggingSpec,
	})

	// Init the MSP
	var mspMgrConfigDir = config.GetPath("peer.mspConfigPath")
	var mspID = viper.GetString("peer.localMspId")
	var mspType = viper.GetString("peer.localMspType")
	if mspType == "" {
		mspType = msp.ProviderTypeToString(msp.FABRIC)
	}
	err = InitCrypto(mspMgrConfigDir, mspID, mspType)
	if err != nil { // Handle errors reading the config file
		mainLogger.Errorf("Cannot run peer because %s", err.Error())
		os.Exit(1)
	}

	runtime.GOMAXPROCS(viper.GetInt("peer.gomaxprocs"))
}
