/*
# Copyright IBM Corp. All Rights Reserved.
#
# SPDX-License-Identifier: Apache-2.0
*/

package platforms

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"strings"

	"github.com/hyperledger/fabric/common/flogging"
	"github.com/hyperledger/fabric/common/metadata"
	cutil "github.com/hyperledger/fabric/core/container/util"
)

//MetadataProvider is implemented by each platform in a platform specific manner.
//It can process metadata stored in ChaincodeDeploymentSpec in different formats.
//The common format is targz. Currently users expect the metadata to be presented
//as tar file entries (directly extracted from chaincode stored in targz format).
//In future, we would like provide better abstraction by extending the interface
type MetadataProvider interface {
	GetMetadataAsTarEntries() ([]byte, error)
}

// Interface for validating the specification and and writing the package for
// the given platform
type Platform interface {
	Name() string
	ValidatePath(path string) error
	ValidateCodePackage(code []byte) error
	GetDeploymentPayload(path string) ([]byte, error)
	GenerateDockerfile() (string, error)
	GenerateDockerBuild(path string, code []byte, tw *tar.Writer) error
	GetMetadataProvider(code []byte) MetadataProvider
}

type PackageWriter interface {
	Write(name string, payload []byte, tw *tar.Writer) error
}

type PackageWriterWrapper func(name string, payload []byte, tw *tar.Writer) error

func (pw PackageWriterWrapper) Write(name string, payload []byte, tw *tar.Writer) error {
	return pw(name, payload, tw)
}

type Registry struct {
	Platforms     map[string]Platform
	PackageWriter PackageWriter
}

var logger = flogging.MustGetLogger("chaincode.platform")

func NewRegistry(platformTypes ...Platform) *Registry {
	platforms := make(map[string]Platform)
	for _, platform := range platformTypes {
		if _, ok := platforms[platform.Name()]; ok {
			logger.Panicf("Multiple platforms of the same name specified: %s", platform.Name())
		}
		platforms[platform.Name()] = platform
	}
	return &Registry{
		Platforms:     platforms,
		PackageWriter: PackageWriterWrapper(cutil.WriteBytesToPackage), //打包
	}
}

func (r *Registry) ValidateSpec(ccType, path string) error {
	platform, ok := r.Platforms[ccType]
	if !ok {
		return fmt.Errorf("Unknown chaincodeType: %s", ccType)
	}
	return platform.ValidatePath(path) //最后实现 根据不同的语言去找对应实现 比如golang 实现：当前目录 ./golang/platform.go No.97 line
}

func (r *Registry) ValidateDeploymentSpec(ccType string, codePackage []byte) error {
	platform, ok := r.Platforms[ccType] //看合约是那种语言写的了  golang  java nodejs
	if !ok {
		return fmt.Errorf("Unknown chaincodeType: %s", ccType)
	}
	return platform.ValidateCodePackage(codePackage) //根据不同语言去验证规范 比如golang 实现方法在： fabric\core\chaincode\platforms\golang\platform.go No.123 line 如果是java 路径golang变成java
}

func (r *Registry) GetMetadataProvider(ccType string, codePackage []byte) (MetadataProvider, error) {
	platform, ok := r.Platforms[ccType]
	if !ok {
		return nil, fmt.Errorf("Unknown chaincodeType: %s", ccType)
	}
	return platform.GetMetadataProvider(codePackage), nil
}

func (r *Registry) GetDeploymentPayload(ccType, path string) ([]byte, error) {
	platform, ok := r.Platforms[ccType]
	if !ok {
		return nil, fmt.Errorf("Unknown chaincodeType: %s", ccType)
	}

	return platform.GetDeploymentPayload(path) //GetDeploymentPayload 实现方法：还是要看platform是什么 如果是golang 那实现方法在 同级目录golang/platform.go No.248 line
}

func (r *Registry) GenerateDockerfile(ccType, name, version string) (string, error) {
	platform, ok := r.Platforms[ccType]
	if !ok {
		return "", fmt.Errorf("Unknown chaincodeType: %s", ccType)
	}

	var buf []string

	// ----------------------------------------------------------------------------------------------------
	// Let the platform define the base Dockerfile
	// ----------------------------------------------------------------------------------------------------
	base, err := platform.GenerateDockerfile()
	if err != nil {
		return "", fmt.Errorf("Failed to generate platform-specific Dockerfile: %s", err)
	}
	buf = append(buf, base)

	// ----------------------------------------------------------------------------------------------------
	// Add some handy labels
	// ----------------------------------------------------------------------------------------------------
	buf = append(buf, fmt.Sprintf(`LABEL %s.chaincode.id.name="%s" \`, metadata.BaseDockerLabel, name))
	buf = append(buf, fmt.Sprintf(`      %s.chaincode.id.version="%s" \`, metadata.BaseDockerLabel, version))
	buf = append(buf, fmt.Sprintf(`      %s.chaincode.type="%s" \`, metadata.BaseDockerLabel, ccType))
	buf = append(buf, fmt.Sprintf(`      %s.version="%s" \`, metadata.BaseDockerLabel, metadata.Version))
	buf = append(buf, fmt.Sprintf(`      %s.base.version="%s"`, metadata.BaseDockerLabel, metadata.BaseVersion))
	// ----------------------------------------------------------------------------------------------------
	// Then augment it with any general options
	// ----------------------------------------------------------------------------------------------------
	//append version so chaincode build version can be compared against peer build version
	buf = append(buf, fmt.Sprintf("ENV CORE_CHAINCODE_BUILDLEVEL=%s", metadata.Version))

	// ----------------------------------------------------------------------------------------------------
	// Finalize it
	// ----------------------------------------------------------------------------------------------------
	contents := strings.Join(buf, "\n")
	logger.Debugf("\n%s", contents)

	return contents, nil
}

func (r *Registry) StreamDockerBuild(ccType, path string, codePackage []byte, inputFiles map[string][]byte, tw *tar.Writer) error {
	var err error

	// ----------------------------------------------------------------------------------------------------
	// Determine our platform driver from the spec
	// ----------------------------------------------------------------------------------------------------
	platform, ok := r.Platforms[ccType]
	if !ok {
		return fmt.Errorf("could not find platform of type: %s", ccType)
	}

	// ----------------------------------------------------------------------------------------------------
	// First stream out our static inputFiles
	// ----------------------------------------------------------------------------------------------------
	for name, data := range inputFiles {
		err = r.PackageWriter.Write(name, data, tw)
		if err != nil {
			return fmt.Errorf(`Failed to inject "%s": %s`, name, err)
		}
	}

	// ----------------------------------------------------------------------------------------------------
	// Now give the platform an opportunity to contribute its own context to the build
	// ----------------------------------------------------------------------------------------------------
	err = platform.GenerateDockerBuild(path, codePackage, tw)
	if err != nil {
		return fmt.Errorf("Failed to generate platform-specific docker build: %s", err)
	}

	return nil
}

func (r *Registry) GenerateDockerBuild(ccType, path, name, version string, codePackage []byte) (io.Reader, error) {

	inputFiles := make(map[string][]byte)

	// ----------------------------------------------------------------------------------------------------
	// Generate the Dockerfile specific to our context
	// ----------------------------------------------------------------------------------------------------
	dockerFile, err := r.GenerateDockerfile(ccType, name, version)
	if err != nil {
		return nil, fmt.Errorf("Failed to generate a Dockerfile: %s", err)
	}

	inputFiles["Dockerfile"] = []byte(dockerFile)

	// ----------------------------------------------------------------------------------------------------
	// Finally, launch an asynchronous process to stream all of the above into a docker build context
	// ----------------------------------------------------------------------------------------------------
	input, output := io.Pipe()

	go func() {
		gw := gzip.NewWriter(output)
		tw := tar.NewWriter(gw)
		err := r.StreamDockerBuild(ccType, path, codePackage, inputFiles, tw)
		if err != nil {
			logger.Error(err)
		}

		tw.Close()
		gw.Close()
		output.CloseWithError(err)
	}()

	return input, nil
}
