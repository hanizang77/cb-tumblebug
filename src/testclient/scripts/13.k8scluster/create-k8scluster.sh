#!/bin/bash

echo "####################################################################"
echo "## 13. K8SCLUSTER: Create"
echo "####################################################################"

source ../init.sh

if [[ -z "${DISK_TYPE[$INDEX,$REGION]}" ]]; then
        RootDiskType="default"
else
        RootDiskType="${DISK_TYPE[$INDEX,$REGION]}"
fi

if [[ -z "${DISK_SIZE[$INDEX,$REGION]}" ]]; then
        RootDiskSize="default"
else
        RootDiskSize="${DISK_SIZE[$INDEX,$REGION]}"
fi

K8SNODEGROUPNAME="ng${INDEX}${REGION}"

echo "CSP=${CSP}"
if [ "${CSP}" == "azure" ] || [ "${CSP}" == "nhncloud" ]; then
    	NODEIMAGEID="" # In azure, image designation is not supported
	echo "NODEIMAGEID=${NODEIMAGEID}"
else
    if [ -n "${CONTAINER_IMAGE_NAME[$INDEX,$REGION]}" ]; then
	    NODEIMAGEID="k8s-${CONN_CONFIG[$INDEX,$REGION]}-${POSTFIX}"
    else
	    NODEIMAGEID="${CONN_CONFIG[$INDEX,$REGION]}-${POSTFIX}"
    fi
fi


if [ -n "${K8S_VERSION[$INDEX,$REGION]}" ]; then
	VERSION=${K8S_VERSION[$INDEX,$REGION]}
else
	echo "You need to specify K8S_VERSION[\$IX,\$IY] in conf.env!!!"
	exit
fi

NUMVM=${OPTION01:-1}
K8SCLUSTERID_ADD=${OPTION03:-1}

K8SCLUSTERID=${K8SCLUSTERID_PREFIX}${INDEX}${REGION}${K8SCLUSTERID_ADD}

DesiredNodeSize=$NUMVM
MinNodeSize="1"
MaxNodeSize=$NUMVM

echo "===================================================================="
echo "CSP=${CSP}"
echo "NSID=${NSID}"
echo "INDEX=${INDEX}"
echo "REGION=${REGION}"
echo "POSTFIX=${POSTFIX}"
echo "NAME=${K8SNODEGROUPNAME}"
echo "IMAGEID=${NODEIMAGEID}"
echo "RootDiskType=${RootDiskType}"
echo "RootDiskSize=${RootDiskSize}"
echo "DesiredNodeSize=${DesiredNodeSize}"
echo "MinNodeSize=${MinNodeSize}"
echo "MaxNodeSize=${MaxNodeSize}"
echo "VERSION=${VERSION}"
echo "K8SCLUSTERID=${K8SCLUSTERID}"
echo "===================================================================="

# Set NodeGroupList for Type-I and Type-II CSP
# https://github.com/cloud-barista/cb-spider/wiki/Provider-Managed-Kubernetes-and-Driver-API#3-%EB%93%9C%EB%9D%BC%EC%9D%B4%EB%B2%84-%EA%B0%9C%EB%B0%9C-%EB%85%B8%ED%8A%B8

if [ "${K8sClusterType[$INDEX]}" == "type1"  ]; then # Type-I CSP
    	K8SNODEGROUPLIST=""
else # Type-II CSP
	K8SNODEGROUPLIST=$(cat <<-END
    		,
                "k8sNodeGroupList": [ {
                        "name": "${K8SNODEGROUPNAME}",
			"imageId": "${NODEIMAGEID}",
			"specId": "${CONN_CONFIG[$INDEX,$REGION]}-${POSTFIX}",
                        "rootDiskType": "${RootDiskType}",
                        "rootDiskSize": "${RootDiskSize}",
                        "sshKeyId": "${CONN_CONFIG[$INDEX,$REGION]}-${POSTFIX}",

			"onAutoScaling": "true",
			"desiredNodeSize": "${DesiredNodeSize}",
			"minNodeSize": "${MinNodeSize}",
			"maxNodeSize": "${MaxNodeSize}"
                        }
		]
END
)
fi 

#                        "imageId": "${NODEIMAGEID}",
#
req=$(cat <<EOF
        {
                "connectionName": "${CONN_CONFIG[$INDEX,$REGION]}",
                "name": "${K8SCLUSTERID}",
                "version": "${VERSION}",
                "vNetId": "${CONN_CONFIG[$INDEX,$REGION]}-${POSTFIX}",
                "subnetIds": [
                        "${CONN_CONFIG[$INDEX,$REGION]}-${POSTFIX}"
                ],
                "securityGroupIds": [
                        "${CONN_CONFIG[$INDEX,$REGION]}-${POSTFIX}"
                ],
                "description": "description"
                ${K8SNODEGROUPLIST}
        }
EOF
        ); echo ${req} | jq '.'

resp=$(
	curl -H "${AUTH}" -sX POST http://$TumblebugServer/tumblebug/ns/$NSID/k8scluster -H 'Content-Type: application/json' -d @- <<EOF
		${req}
EOF
    ); echo ${resp} | jq '.'
    echo ""
   
# nodeGroupList.Name: 12 or fewer characters in Linux, 8 or fewer characters in Windows, only lowercase
# https://aka.ms/aks-naming-rules
# The AKS node or MC_ resource group name combines the resource group name and resource name. The autogenerated syntax of MC_resourceGroupName_resourceName_AzureRegion must be no greater than 80 characters in length
# CB-Spider adds 20 characters postfix to K8SCLUSTER's name
