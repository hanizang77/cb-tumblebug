/*
Copyright 2019 The Cloud-Barista Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package resource is to manage multi-cloud infra resource
package resource

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/rs/zerolog/log"

	"github.com/cloud-barista/cb-tumblebug/src/core/common"
	clientManager "github.com/cloud-barista/cb-tumblebug/src/core/common/client"
	"github.com/cloud-barista/cb-tumblebug/src/core/model"
	"github.com/cloud-barista/cb-tumblebug/src/kvstore/kvstore"

	validator "github.com/go-playground/validator/v10"
)

// TbImageReqStructLevelValidation func is for Validation
func TbImageReqStructLevelValidation(sl validator.StructLevel) {

	u := sl.Current().Interface().(model.TbImageReq)

	err := common.CheckString(u.Name)
	if err != nil {
		// ReportError(field interface{}, fieldName, structFieldName, tag, param string)
		sl.ReportError(u.Name, "name", "Name", err.Error(), "")
	}
}

// ConvertSpiderImageToTumblebugImage accepts an Spider image object, converts to and returns an TB image object
func ConvertSpiderImageToTumblebugImage(spiderImage model.SpiderImageInfo) (model.TbImageInfo, error) {
	if spiderImage.IId.NameId == "" {
		err := fmt.Errorf("ConvertSpiderImageToTumblebugImage failed; spiderImage.IId.NameId == EmptyString")
		emptyTumblebugImage := model.TbImageInfo{}
		return emptyTumblebugImage, err
	}

	tumblebugImage := model.TbImageInfo{}
	//tumblebugImage.Id = spiderImage.IId.NameId

	spiderKeyValueListName := common.LookupKeyValueList(spiderImage.KeyValueList, "Name")
	if len(spiderKeyValueListName) > 0 {
		tumblebugImage.Name = spiderKeyValueListName
	} else {
		tumblebugImage.Name = spiderImage.IId.NameId
	}

	tumblebugImage.CspImageName = spiderImage.IId.NameId
	tumblebugImage.Description = common.LookupKeyValueList(spiderImage.KeyValueList, "Description")
	tumblebugImage.CreationDate = common.LookupKeyValueList(spiderImage.KeyValueList, "CreationDate")
	// GuestOS should be refinded, spiderImage.OSDistribution value is not ideal for GuestOS
	tumblebugImage.GuestOS = spiderImage.OSDistribution
	tumblebugImage.Architecture = string(spiderImage.OSArchitecture)
	tumblebugImage.Platform = string(spiderImage.OSPlatform)
	tumblebugImage.Distribution = spiderImage.OSDistribution
	tumblebugImage.RootDiskType = spiderImage.OSDiskType
	rootDiskMinSizeGB, _ := strconv.ParseFloat(spiderImage.OSDiskSizeGB, 32)
	tumblebugImage.RootDiskMinSizeGB = float32(rootDiskMinSizeGB)

	tumblebugImage.Status = string(spiderImage.ImageStatus)
	tumblebugImage.KeyValueList = spiderImage.KeyValueList

	return tumblebugImage, nil
}

// GetImageInfoFromLookupImage
func GetImageInfoFromLookupImage(nsId string, u model.TbImageReq) (model.TbImageInfo, error) {
	content := model.TbImageInfo{}
	res, err := LookupImage(u.ConnectionName, u.CspImageName)
	if err != nil {
		log.Trace().Err(err).Msg("")
		return content, err
	}
	if res.IId.NameId == "" {
		err := fmt.Errorf("spider returned empty IId.NameId without Error: %s", u.ConnectionName)
		log.Error().Err(err).Msgf("Cannot LookupImage %s %v", u.CspImageName, res)
		return content, err
	}

	content, err = ConvertSpiderImageToTumblebugImage(res)
	if err != nil {
		log.Error().Err(err).Msg("")
		return content, err
	}
	content.Namespace = nsId
	content.ConnectionName = u.ConnectionName
	content.Id = u.Name
	content.Name = u.Name
	content.AssociatedObjectList = []string{}

	return content, nil
}

// RegisterImageWithInfoInBulk register a list of images in bulk
func RegisterImageWithInfoInBulk(imageList []model.TbImageInfo) error {
	// Insert in bulk
	// batch size is 90 due to the limit of SQL
	batchSize := 90

	total := len(imageList)
	for i := 0; i < total; i += batchSize {
		end := i + batchSize
		if end > total {
			end = total
		}
		batch := imageList[i:end]

		session := model.ORM.NewSession()
		defer session.Close()
		if err := session.Begin(); err != nil {
			log.Error().Err(err).Msg("Failed to begin transaction")
			return err
		}

		affected, err := session.Insert(&batch)
		if err != nil {
			session.Rollback()
			log.Error().Err(err).Msg("Error inserting images in bulk")
			return err
		} else {
			if err := session.Commit(); err != nil {
				log.Error().Err(err).Msg("Failed to commit transaction")
				return err
			}
			log.Trace().Msgf("Bulk insert success: %d records affected", affected)
		}
	}
	return nil
}

// RemoveDuplicateImagesInSQL is to remove duplicate images in db to refine batch insert duplicates
func RemoveDuplicateImagesInSQL() error {
	sqlStr := `
	DELETE FROM TbImageInfo
	WHERE rowid NOT IN (
		SELECT MAX(rowid)
		FROM TbImageInfo
		GROUP BY Namespace, Id
	);
	`
	_, err := model.ORM.Exec(sqlStr)
	if err != nil {
		log.Error().Err(err).Msg("Error deleting duplicate images")
		return err
	}
	log.Info().Msg("Duplicate images removed successfully")

	return nil
}

// RegisterImageWithId accepts image creation request, creates and returns an TB image object
func RegisterImageWithId(nsId string, u *model.TbImageReq, update bool, RDBonly bool) (model.TbImageInfo, error) {

	content := model.TbImageInfo{}

	err := common.CheckString(nsId)
	if err != nil {
		log.Error().Err(err).Msg("")
		return content, err
	}

	resourceType := model.StrImage
	if !RDBonly {
		check, err := CheckResource(nsId, resourceType, u.Name)
		if !update {
			if check {
				err := fmt.Errorf("The image " + u.Name + " already exists.")
				return content, err
			}
		}
		if err != nil {
			err := fmt.Errorf("Failed to check the existence of the image " + u.Name + ".")
			return content, err
		}
	}

	res, err := LookupImage(u.ConnectionName, u.CspImageName)
	if err != nil {
		log.Trace().Err(err).Msg("")
		return content, err
	}
	if res.IId.NameId == "" {
		err := fmt.Errorf("CB-Spider returned empty IId.NameId without Error: %s", u.ConnectionName)
		log.Error().Err(err).Msgf("Cannot LookupImage %s %v", u.CspImageName, res)
		return content, err
	}

	content, err = ConvertSpiderImageToTumblebugImage(res)
	if err != nil {
		log.Error().Err(err).Msg("")
		//err := fmt.Errorf("an error occurred while converting Spider image info to Tumblebug image info.")
		return content, err
	}
	content.Namespace = nsId
	content.ConnectionName = u.ConnectionName
	content.Id = u.Name
	content.Name = u.Name
	content.AssociatedObjectList = []string{}

	if !RDBonly {
		Key := common.GenResourceKey(nsId, resourceType, content.Id)
		Val, _ := json.Marshal(content)
		err = kvstore.Put(Key, string(Val))
		if err != nil {
			log.Error().Err(err).Msg("")
			return content, err
		}
	}

	// "INSERT INTO `image`(`namespace`, `id`, ...) VALUES ('nsId', 'content.Id', ...);
	// Attempt to insert the new record
	_, err = model.ORM.Insert(content)
	if err != nil {
		if update {
			// If insert fails and update is true, attempt to update the existing record
			_, updateErr := model.ORM.Update(content, &model.TbImageInfo{Namespace: content.Namespace, Id: content.Id})
			if updateErr != nil {
				log.Error().Err(updateErr).Msg("Error updating image after insert failure")
				return content, updateErr
			} else {
				log.Trace().Msg("SQL: Update success after insert failure")
			}
		} else {
			log.Error().Err(err).Msg("Error inserting image and update flag is false")
			return content, err
		}
	} else {
		log.Trace().Msg("SQL: Insert success")
	}

	return content, nil
}

// RegisterImageWithInfo accepts image creation request, creates and returns an TB image object
func RegisterImageWithInfo(nsId string, content *model.TbImageInfo, update bool) (model.TbImageInfo, error) {

	resourceType := model.StrImage

	err := common.CheckString(nsId)
	if err != nil {
		log.Error().Err(err).Msg("")
		return model.TbImageInfo{}, err
	}
	// err = common.CheckString(content.Name)
	// if err != nil {
	// 	log.Error().Err(err).Msg("")
	// 	return model.TbImageInfo{}, err
	// }
	check, err := CheckResource(nsId, resourceType, content.Name)

	if !update {
		if check {
			err := fmt.Errorf("The image " + content.Name + " already exists.")
			return model.TbImageInfo{}, err
		}
	}

	if err != nil {
		err := fmt.Errorf("Failed to check the existence of the image " + content.Name + ".")
		return model.TbImageInfo{}, err
	}

	content.Namespace = nsId
	//content.Id = common.GenUid()
	content.Id = content.Name
	content.AssociatedObjectList = []string{}

	log.Info().Msg("PUT registerImage")
	Key := common.GenResourceKey(nsId, resourceType, content.Id)
	Val, _ := json.Marshal(content)
	err = kvstore.Put(Key, string(Val))
	if err != nil {
		log.Error().Err(err).Msg("")
		return model.TbImageInfo{}, err
	}

	// "INSERT INTO `image`(`namespace`, `id`, ...) VALUES ('nsId', 'content.Id', ...);
	_, err = model.ORM.Insert(content)
	if err != nil {
		log.Error().Err(err).Msg("")
	} else {
		log.Trace().Msg("SQL: Insert success")
	}

	return *content, nil
}

// LookupImageList accepts Spider conn config,
// lookups and returns the list of all images in the region of conn config
// in the form of the list of Spider image objects
func LookupImageList(connConfigName string) (model.SpiderImageList, error) {

	var callResult model.SpiderImageList
	client := resty.New()
	url := model.SpiderRestUrl + "/vmimage"
	method := "GET"
	requestBody := model.SpiderConnectionName{}
	requestBody.ConnectionName = connConfigName

	err := clientManager.ExecuteHttpRequest(
		client,
		method,
		url,
		nil,
		clientManager.SetUseBody(requestBody),
		&requestBody,
		&callResult,
		clientManager.ShortDuration,
	)

	if err != nil {
		log.Err(err).Msg("Failed to Lookup Image List from Spider")
		return callResult, err
	}
	return callResult, nil
}

// LookupImage accepts Spider conn config and CSP image ID, lookups and returns the Spider image object
func LookupImage(connConfig string, imageId string) (model.SpiderImageInfo, error) {

	if connConfig == "" {
		content := model.SpiderImageInfo{}
		err := fmt.Errorf("LookupImage() called with empty connConfig.")
		log.Error().Err(err).Msg("")
		return content, err
	} else if imageId == "" {
		content := model.SpiderImageInfo{}
		err := fmt.Errorf("LookupImage() called with empty imageId.")
		log.Error().Err(err).Msg("")
		return content, err
	}

	client := resty.New()
	client.SetTimeout(2 * time.Minute)
	url := model.SpiderRestUrl + "/vmimage/" + url.QueryEscape(imageId)
	method := "GET"
	requestBody := model.SpiderConnectionName{}
	requestBody.ConnectionName = connConfig
	callResult := model.SpiderImageInfo{}

	err := clientManager.ExecuteHttpRequest(
		client,
		method,
		url,
		nil,
		clientManager.SetUseBody(requestBody),
		&requestBody,
		&callResult,
		clientManager.MediumDuration,
	)

	if err != nil {
		log.Trace().Err(err).Msg("")
		return callResult, err
	}

	return callResult, nil
}

// FetchImagesForAllConnConfigs gets all conn configs from Spider, lookups all images for each region of conn config, and saves into TB image objects
func FetchImagesForConnConfig(connConfig string, nsId string) (imageCount uint, err error) {
	log.Debug().Msg("FetchImagesForConnConfig(" + connConfig + ")")

	spiderImageList, err := LookupImageList(connConfig)
	if err != nil {
		log.Error().Err(err).Msg("")
		return 0, err
	}

	for _, spiderImage := range spiderImageList.Image {
		tumblebugImage, err := ConvertSpiderImageToTumblebugImage(spiderImage)
		if err != nil {
			log.Error().Err(err).Msg("")
			return 0, err
		}

		tumblebugImageId := connConfig + "-" + ToNamingRuleCompatible(tumblebugImage.Name)

		check, err := CheckResource(nsId, model.StrImage, tumblebugImageId)
		if check {
			log.Info().Msgf("The image %s already exists in TB; continue", tumblebugImageId)
			continue
		} else if err != nil {
			log.Info().Msgf("Cannot check the existence of %s in TB; continue", tumblebugImageId)
			continue
		} else {
			tumblebugImage.Name = tumblebugImageId
			tumblebugImage.ConnectionName = connConfig

			_, err := RegisterImageWithInfo(nsId, &tumblebugImage, true)
			if err != nil {
				log.Error().Err(err).Msg("")
				return 0, err
			}
			imageCount++
		}
	}
	return imageCount, nil
}

// FetchImagesForAllConnConfigs gets all conn configs from Spider, lookups all images for each region of conn config, and saves into TB image objects
func FetchImagesForAllConnConfigs(nsId string) (connConfigCount uint, imageCount uint, err error) {

	err = common.CheckString(nsId)
	if err != nil {
		log.Error().Err(err).Msg("")
		return 0, 0, err
	}

	connConfigs, err := common.GetConnConfigList(model.DefaultCredentialHolder, true, true)
	if err != nil {
		log.Error().Err(err).Msg("")
		return 0, 0, err
	}

	for _, connConfig := range connConfigs.Connectionconfig {
		temp, _ := FetchImagesForConnConfig(connConfig.ConfigName, nsId)
		imageCount += temp
		connConfigCount++
	}
	return connConfigCount, imageCount, nil
}

// SearchImage accepts arbitrary number of keywords, and returns the list of matched TB image objects
func SearchImage(nsId string, keywords ...string) ([]model.TbImageInfo, error) {

	err := common.CheckString(nsId)
	if err != nil {
		log.Error().Err(err).Msg("")
		return nil, err
	}

	tempList := []model.TbImageInfo{}

	//sqlQuery := "SELECT * FROM `image` WHERE `namespace`='" + nsId + "'"
	sqlQuery := model.ORM.Where("Namespace = ?", nsId)

	for _, keyword := range keywords {
		keyword = ToNamingRuleCompatible(keyword)
		//sqlQuery += " AND `name` LIKE '%" + keyword + "%'"
		sqlQuery = sqlQuery.And("Name LIKE ?", "%"+keyword+"%")
	}

	err = sqlQuery.Find(&tempList)
	if err != nil {
		log.Error().Err(err).Msg("")
		return tempList, err
	}
	return tempList, nil
}

// UpdateImage accepts to-be TB image objects,
// updates and returns the updated TB image objects
func UpdateImage(nsId string, imageId string, fieldsToUpdate model.TbImageInfo, RDBonly bool) (model.TbImageInfo, error) {
	if !RDBonly {

		resourceType := model.StrImage
		temp := model.TbImageInfo{}
		err := common.CheckString(nsId)
		if err != nil {
			log.Error().Err(err).Msg("")
			return temp, err
		}

		if len(fieldsToUpdate.Namespace) > 0 {
			err := fmt.Errorf("You should not specify 'namespace' in the JSON request body.")
			log.Error().Err(err).Msg("")
			return temp, err
		}

		if len(fieldsToUpdate.Id) > 0 {
			err := fmt.Errorf("You should not specify 'id' in the JSON request body.")
			log.Error().Err(err).Msg("")
			return temp, err
		}

		check, err := CheckResource(nsId, resourceType, imageId)
		if err != nil {
			log.Error().Err(err).Msg("")
			return temp, err
		}

		if !check {
			err := fmt.Errorf("The image " + imageId + " does not exist.")
			return temp, err
		}

		tempInterface, err := GetResource(nsId, resourceType, imageId)
		if err != nil {
			err := fmt.Errorf("Failed to get the image " + imageId + ".")
			return temp, err
		}
		asIsImage := model.TbImageInfo{}
		err = common.CopySrcToDest(&tempInterface, &asIsImage)
		if err != nil {
			err := fmt.Errorf("Failed to CopySrcToDest() " + imageId + ".")
			return temp, err
		}

		// Update specified fields only
		toBeImage := asIsImage
		toBeImageJSON, _ := json.Marshal(fieldsToUpdate)
		err = json.Unmarshal(toBeImageJSON, &toBeImage)

		Key := common.GenResourceKey(nsId, resourceType, toBeImage.Id)
		Val, _ := json.Marshal(toBeImage)
		err = kvstore.Put(Key, string(Val))
		if err != nil {
			log.Error().Err(err).Msg("")
			return temp, err
		}

	}
	// "UPDATE `image` SET `id`='" + imageId + "', ... WHERE `namespace`='" + nsId + "' AND `id`='" + imageId + "';"
	_, err := model.ORM.Update(&fieldsToUpdate, &model.TbImageInfo{Namespace: nsId, Id: imageId})
	if err != nil {
		log.Error().Err(err).Msg("")
		return fieldsToUpdate, err
	} else {
		log.Trace().Msg("SQL: Update success")
	}

	return fieldsToUpdate, nil
}

// GetImage accepts namespace Id and imageKey(Id,CspResourceName,GuestOS,...), and returns the TB image object
func GetImage(nsId string, imageKey string) (model.TbImageInfo, error) {
	if err := common.CheckString(nsId); err != nil {
		log.Error().Err(err).Msg("Invalid namespace ID")
		return model.TbImageInfo{}, err
	}

	log.Debug().Msg("[Get image] " + imageKey)

	// make comparison case-insensitive
	nsId = strings.ToLower(nsId)
	imageKey = strings.ToLower(imageKey)
	imageKey = strings.ReplaceAll(imageKey, " ", "")

	providerName, regionName, _, resourceName, err := ResolveProviderRegionZoneResourceKey(imageKey)
	if err != nil {
		// imageKey does not include information for providerName, regionName
		image := model.TbImageInfo{Namespace: nsId, Id: imageKey}

		// 1) Check if the image is a custom image
		// ex: custom-img-487zeit5
		tempInterface, err := GetResource(nsId, model.StrCustomImage, imageKey)
		customImage := model.TbCustomImageInfo{}
		if err == nil {
			err = common.CopySrcToDest(&tempInterface, &customImage)
			if err != nil {
				log.Error().Err(err).Msg("TbCustomImageInfo CopySrcToDest error")
				return model.TbImageInfo{}, err
			}
			image.CspImageName = customImage.CspResourceName
			image.SystemLabel = model.StrCustomImage
			return image, nil
		}

		// 2) Check if the image is a registered image in the given namespace
		// ex: img-487zeit5
		image = model.TbImageInfo{Namespace: nsId, Id: imageKey}
		has, err := model.ORM.Where("LOWER(Namespace) = ? AND LOWER(Id) = ?", nsId, imageKey).Get(&image)
		if err != nil {
			log.Info().Err(err).Msgf("Cannot get image %s by ID from %s", imageKey, nsId)
		}
		if has {
			return image, nil
		}

	} else {
		// imageKey includes information for providerName, regionName

		// 1) Check if the image is a registered image in the common namespace model.SystemCommonNs by ImageId
		// ex: tencent+ap-jakarta+ubuntu22.04 or tencent+ap-jakarta+img-487zeit5
		image := model.TbImageInfo{Namespace: model.SystemCommonNs, Id: imageKey}
		has, err := model.ORM.Where("LOWER(Namespace) = ? AND LOWER(Id) = ?", model.SystemCommonNs, imageKey).Get(&image)
		if err != nil {
			log.Info().Err(err).Msgf("Cannot get image %s by ID from %s", imageKey, model.SystemCommonNs)
		}
		if has {
			return image, nil
		}

		// 2) Check if the image is a registered image in the common namespace model.SystemCommonNs by CspImageName
		// ex: tencent+ap-jakarta+img-487zeit5
		image = model.TbImageInfo{Namespace: model.SystemCommonNs, CspImageName: resourceName}
		has, err = model.ORM.Where("LOWER(Namespace) = ? AND LOWER(CspImageName) = ? AND LOWER(Id) LIKE ? AND LOWER(Id) LIKE ?",
			model.SystemCommonNs,
			resourceName,
			"%"+strings.ToLower(providerName)+"%",
			"%"+strings.ToLower(regionName)+"%").Get(&image)
		if err != nil {
			log.Info().Err(err).Msgf("Cannot get image %s by CspImageName", resourceName)
		}
		if has {
			return image, nil
		}

		// 3) Check if the image is a registered image in the common namespace model.SystemCommonNs by GuestOS
		// ex: tencent+ap-jakarta+Ubuntu22.04
		image = model.TbImageInfo{Namespace: model.SystemCommonNs, GuestOS: resourceName}
		has, err = model.ORM.Where("LOWER(Namespace) = ? AND LOWER(GuestOS) LIKE ? AND LOWER(Id) LIKE ? AND LOWER(Id) LIKE ?",
			model.SystemCommonNs,
			"%"+strings.ToLower(resourceName)+"%",
			"%"+strings.ToLower(providerName)+"%",
			"%"+strings.ToLower(regionName)+"%").Get(&image)
		if err != nil {
			log.Info().Err(err).Msgf("Failed to get image %s by GuestOS type", resourceName)
		}
		if has {
			return image, nil
		}

	}

	return model.TbImageInfo{}, fmt.Errorf("The imageKey %s not found by any of ID, CspImageName, GuestOS", imageKey)
}
