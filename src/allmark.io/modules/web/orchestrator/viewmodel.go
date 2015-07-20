// Copyright 2014 Andreas Koch. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package orchestrator

import (
	"time"

	"allmark.io/modules/common/route"
	"allmark.io/modules/model"
	"allmark.io/modules/web/view/viewmodel"
)

type ViewModelOrchestrator struct {
	*Orchestrator

	navigationOrchestrator *NavigationOrchestrator
	tagOrchestrator        *TagsOrchestrator
	fileOrchestrator       *FileOrchestrator

	// caches (do not initialize!)
	latestByRoute         map[string][]*viewmodel.Model
	viewmodelsByRoute     map[string]*viewmodel.Model
	fullViewmodelsByRoute map[string]*viewmodel.Model
}

// GetFullViewModel returns a fully-initialized viewmodel for the given route.
func (orchestrator *ViewModelOrchestrator) GetFullViewModel(itemRoute route.Route) (viewmodel.Model, bool) {

	// return from cache
	if orchestrator.fullViewmodelsByRoute != nil {

		if viewModel := orchestrator.fullViewmodelsByRoute[itemRoute.String()]; viewModel != nil {

			// append the content
			viewModel.Content = orchestrator.getHTMLFromRoute(itemRoute)

			return *viewModel, true
		}

		return viewmodel.Model{}, false
	}

	// initialize the cache
	orchestrator.fullViewmodelsByRoute = make(map[string]*viewmodel.Model)

	// updateViewModel update the viewmodel cache for the given route.
	updateViewModel := func(route route.Route) {

		// get the requested item
		item := orchestrator.getItem(route)
		if item == nil {
			return
		}

		// get the base view model
		viewModel := orchestrator.getViewModel(route)
		if viewModel == nil {
			return
		}

		// navigation
		viewModel.ToplevelNavigation = orchestrator.navigationOrchestrator.GetToplevelNavigation()
		viewModel.BreadcrumbNavigation = orchestrator.navigationOrchestrator.GetBreadcrumbNavigation(route)
		viewModel.ItemNavigation = orchestrator.navigationOrchestrator.GetItemNavigation(route)

		// childs
		viewModel.Childs = orchestrator.getChildModels(route)

		// tags
		viewModel.Tags = orchestrator.tagOrchestrator.getItemTags(route)

		// Geo Coordinates
		viewModel.GeoLocation = getGeoLocation(item)

		// Analytics Settings
		viewModel.Analytics = orchestrator.getAnalyticsSettings()

		// Hash / ETag
		viewModel.Hash = item.Hash

		// special viewmodel attributes
		isRepositoryItem := item.Type == model.TypeRepository
		if isRepositoryItem {

			// tag cloud
			repositoryIsNotEmpty := orchestrator.index().Size() >= 5 // don't bother to create a tag cloud if there aren't enough documents
			if repositoryIsNotEmpty {

				tagCloud := orchestrator.tagOrchestrator.GetTagCloud()
				viewModel.TagCloud = tagCloud

			}

		}

		orchestrator.fullViewmodelsByRoute[route.String()] = viewModel
	}

	// deleteViewModel removed the viewmodel with the given route from the cache.
	deleteViewModel := func(route route.Route) {
		delete(orchestrator.fullViewmodelsByRoute, route.String())
	}

	// write the cache for the requested route directly
	updateViewModel(itemRoute)

	// write cache for all other routes async
	go func() {
		startTime := time.Now()

		for _, childRoute := range orchestrator.repository.Routes() {
			updateViewModel(childRoute)
		}

		endTime := time.Now()
		duration := endTime.Sub(startTime)
		orchestrator.logger.Statistics("Priming the full view model cache took %f seconds.", duration.Seconds())
	}()

	// register update callbacks
	orchestrator.registerUpdateCallback("update full viewmodel", UpdateTypeNew, updateViewModel)
	orchestrator.registerUpdateCallback("update full viewmodel", UpdateTypeModified, updateViewModel)
	orchestrator.registerUpdateCallback("update full viewmodel", UpdateTypeDeleted, deleteViewModel)

	return orchestrator.GetFullViewModel(itemRoute)
}

func (orchestrator *ViewModelOrchestrator) GetViewModel(itemRoute route.Route) (viewModel viewmodel.Model, found bool) {

	vm := orchestrator.getViewModel(itemRoute)
	if vm == nil {
		return viewmodel.Model{}, false
	}

	// append the content
	vm.Content = orchestrator.getHTMLFromRoute(itemRoute)

	return *vm, true
}

// GetViewModelByAlias returns the viewmodel by its alias.
func (orchestrator *ViewModelOrchestrator) GetViewModelByAlias(alias string) (viewModel viewmodel.Model, found bool) {

	item := orchestrator.getItemByAlias(alias)
	if item == nil {
		return viewmodel.Model{}, false
	}

	vm := orchestrator.getViewModel(item.Route())
	if vm == nil {
		return viewmodel.Model{}, false
	}

	// append the content
	vm.Content = orchestrator.getHTMLFromRoute(item.Route())

	return *vm, true
}

// GetLatest returns the latest items (sorted by creation date) for the given route.
func (orchestrator *ViewModelOrchestrator) GetLatest(itemRoute route.Route, pageSize, page int) (latest []*viewmodel.Model, found bool) {

	// return from cache if cache has been initialized
	if orchestrator.latestByRoute != nil {

		if models, exists := orchestrator.latestByRoute[itemRoute.Value()]; exists {

			// get the paged view models
			latestModels, found := pagedViewmodels(models, pageSize, page)
			if !found {
				return []*viewmodel.Model{}, false
			}

			// convert the content
			for _, model := range latestModels {
				itemRoute := route.NewFromRequest(model.Route)

				// convert to html
				content := orchestrator.getHTMLFromRoute(itemRoute)

				// lazy-load
				content = lazyLoad(content)

				// attach to model
				model.Content = content
			}

			return latestModels, true

		}

		return []*viewmodel.Model{}, false

	}

	// updateLatest updates the latest items for the given route.
	updateLatest := func(route route.Route) {
		startTime := time.Now()

		orchestrator.latestByRoute = make(map[string][]*viewmodel.Model)
		for _, childRoute := range orchestrator.repository.Routes() {
			latestItems := orchestrator.getLatestItems(childRoute)
			orchestrator.latestByRoute[childRoute.Value()] = orchestrator.getLastesViewModelsFromItemList(latestItems)
		}

		// log timing reports
		endTime := time.Now()
		duration := endTime.Sub(startTime)
		orchestrator.logger.Statistics("Priming the latest items cache took %f seconds.", duration.Seconds())
	}

	// asyncUpdateLatest executes updateLatest for the given route in a go routine.
	asyncUpdateLatest := func(route route.Route) {
		go updateLatest(route)
	}

	// initialize cache
	updateLatest(route.New())

	// register update callbacks
	orchestrator.registerUpdateCallback("update latest", UpdateTypeNew, asyncUpdateLatest)
	orchestrator.registerUpdateCallback("update latest", UpdateTypeModified, asyncUpdateLatest)
	orchestrator.registerUpdateCallback("update latest", UpdateTypeDeleted, asyncUpdateLatest)

	// return the result
	return orchestrator.GetLatest(itemRoute, pageSize, page)
}

// Converts a list of model.Item elements into a view models for the latest-items controller
func (orchestrator *ViewModelOrchestrator) getLastesViewModelsFromItemList(items []*model.Item) []*viewmodel.Model {

	// create viewmodels
	models := make([]*viewmodel.Model, 0, len(items))
	for _, item := range items {

		viewModel := orchestrator.getViewModel(item.Route())
		if viewModel == nil {
			orchestrator.logger.Error("No view model found for item %q.", item.String())
			continue
		}

		models = append(models, viewModel)
	}

	return models
}

func (orchestrator *ViewModelOrchestrator) getViewModel(itemRoute route.Route) *viewmodel.Model {

	if orchestrator.viewmodelsByRoute != nil {
		return orchestrator.viewmodelsByRoute[itemRoute.String()]
	}

	// updateViewModel stores the view model for the given route to the cache
	updateViewModel := func(route route.Route) {

		// convert content
		item := orchestrator.getItem(route)
		if item == nil {
			orchestrator.logger.Warn("Cannot update viewmodel cache. The item with the route %q was not found.", route.String())
			return
		}

		root := orchestrator.rootItem()

		viewModel := &viewmodel.Model{
			Base:             getBaseModel(root, item, orchestrator.itemPather(), orchestrator.config),
			Content:          "", // convert later
			Markdown:         item.Markdown,
			Publisher:        orchestrator.getPublisherInformation(),
			Author:           orchestrator.getAuthorInformation(item.MetaData.Author),
			Files:            orchestrator.fileOrchestrator.GetFiles(route),
			Images:           orchestrator.fileOrchestrator.GetImages(route),
			IsRepositoryItem: true,
		}

		// add rft url if rtf conversion is enabled
		if orchestrator.config.Conversion.RTF.IsEnabled() {
			viewModel.RTFURL = GetTypedItemURL(route, "rtf")
		}

		orchestrator.viewmodelsByRoute[route.String()] = viewModel
	}

	// deleteViewModel stores the view model for the given route from the cache
	deleteViewModel := func(route route.Route) {
		delete(orchestrator.viewmodelsByRoute, route.String())
	}

	// build the cache
	orchestrator.viewmodelsByRoute = make(map[string]*viewmodel.Model)
	for _, item := range orchestrator.index().GetAllItems() {
		updateViewModel(item.Route())
	}

	// register update callbacks
	orchestrator.registerUpdateCallback("update viewmodel", UpdateTypeNew, updateViewModel)
	orchestrator.registerUpdateCallback("update viewmodel", UpdateTypeModified, updateViewModel)
	orchestrator.registerUpdateCallback("update viewmodel", UpdateTypeDeleted, deleteViewModel)

	return orchestrator.viewmodelsByRoute[itemRoute.String()]
}

func (orchestrator *ViewModelOrchestrator) getChildModels(itemRoute route.Route) []*viewmodel.Base {

	rootItem := orchestrator.rootItem()
	if rootItem == nil {
		orchestrator.logger.Fatal("No root item found")
	}

	pathProvider := orchestrator.relativePather(itemRoute)

	childModels := make([]*viewmodel.Base, 0)
	childItems := orchestrator.getChilds(itemRoute)
	for _, childItem := range childItems {
		baseModel := getBaseModel(rootItem, childItem, pathProvider, orchestrator.config)
		childModels = append(childModels, &baseModel)
	}

	// sort the models
	viewmodel.SortBaseModelBy(sortBaseModelsByDate).Sort(childModels)

	return childModels
}

// getHTMLFromItem returns the converted HTML code for the given item model.
func (orchestrator *ViewModelOrchestrator) getHTMLFromItem(item *model.Item) string {
	if item == nil {
		return ""
	}

	convertedContent, err := orchestrator.converter.Convert(orchestrator.getItemByAlias, orchestrator.absolutePather("/"), orchestrator.relativePather(item.Route()), item)
	if err != nil {
		orchestrator.logger.Warn("Cannot convert content for route %q. Error: %s.", item.Route(), err.Error())
		return "<!-- Conversion Error -->"
	}

	return convertedContent
}

// getHTMLFromRoute returns the converted HTML code for the item with the given route.
func (orchestrator *ViewModelOrchestrator) getHTMLFromRoute(route route.Route) string {
	item := orchestrator.getItem(route)
	if item == nil {
		return ""
	}

	return orchestrator.getHTMLFromItem(item)
}
