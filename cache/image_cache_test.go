package cache_test

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/buildpacks/imgutil/fakes"
	"github.com/buildpacks/imgutil/local"
	"github.com/sclevine/spec"
	"github.com/sclevine/spec/report"

	"github.com/buildpacks/lifecycle/buildpack"
	"github.com/buildpacks/lifecycle/cache"
	"github.com/buildpacks/lifecycle/platform"
	h "github.com/buildpacks/lifecycle/testhelpers"
)

func TestImageCache(t *testing.T) {
	rand.Seed(time.Now().UTC().UnixNano())
	spec.Run(t, "ImageCache", testImageCache, spec.Parallel(), spec.Report(report.Terminal{}))
}

func testImageCache(t *testing.T, when spec.G, it spec.S) {
	var (
		tmpDir            string
		fakeOriginalImage *fakes.Image
		fakeNewImage      *fakes.Image
		subject           *cache.ImageCache
		testLayerTarPath  string
		testLayerSHA      string
	)

	it.Before(func() {
		var err error

		tmpDir, err = ioutil.TempDir("", "")
		h.AssertNil(t, err)

		fakeOriginalImage = fakes.NewImage("fake-image", "", local.IDIdentifier{ImageID: "fakeOriginalImage"})
		fakeNewImage = fakes.NewImage("fake-image", "", local.IDIdentifier{ImageID: "fakeImage"})

		subject = cache.NewImageCache(fakeOriginalImage, fakeNewImage)
		h.AssertNil(t, err)

		testLayerTarPath = filepath.Join(tmpDir, "some-layer.tar")
		h.AssertNil(t, ioutil.WriteFile(testLayerTarPath, []byte("dummy data"), 0600))
		testLayerSHA = "sha256:" + h.ComputeSHA256ForFile(t, testLayerTarPath)
	})

	it.After(func() {
		h.AssertNil(t, fakeOriginalImage.Cleanup())
		h.AssertNil(t, fakeNewImage.Cleanup())
		h.AssertNil(t, os.RemoveAll(tmpDir))
	})

	when("#Name", func() {
		it("returns the image name", func() {
			h.AssertEq(t, subject.Name(), "fake-image")
		})
	})

	when("#RetrieveMetadata", func() {
		when("original image contains valid metadata", func() {
			it.Before(func() {
				h.AssertNil(t, fakeOriginalImage.SetLabel(
					"io.buildpacks.lifecycle.cache.metadata",
					`{"buildpacks": [{"key": "bp.id", "version": "1.2.3", "layers": {"some-layer": {"sha": "some-sha", "data": "some-data", "build": true, "launch": false, "cache": true}}}]}`,
				))
			})

			it("returns the metadata", func() {
				expected := platform.CacheMetadata{
					Buildpacks: []platform.BuildpackLayersMetadata{{
						ID:      "bp.id",
						Version: "1.2.3",
						Layers: map[string]platform.BuildpackLayerMetadata{
							"some-layer": {
								LayerMetadata: platform.LayerMetadata{
									SHA: "some-sha",
								},
								LayerMetadataFile: buildpack.LayerMetadataFile{
									Data:   "some-data",
									Build:  true,
									Launch: false,
									Cache:  true,
								},
							},
						},
					}},
				}

				meta, err := subject.RetrieveMetadata()
				h.AssertNil(t, err)
				h.AssertEq(t, meta, expected)
			})
		})

		when("original image contains invalid metadata", func() {
			it.Before(func() {
				h.AssertNil(t, fakeOriginalImage.SetLabel("io.buildpacks.lifecycle.cache.metadata", "garbage"))
			})

			it("returns empty metadata", func() {
				meta, err := subject.RetrieveMetadata()
				h.AssertNil(t, err)
				h.AssertEq(t, len(meta.Buildpacks), 0)
			})
		})

		when("original image metadata label missing", func() {
			it("returns empty metadata", func() {
				meta, err := subject.RetrieveMetadata()
				h.AssertNil(t, err)
				h.AssertEq(t, len(meta.Buildpacks), 0)
			})
		})
	})

	when("#RetrieveLayer", func() {
		when("layer exists", func() {
			it.Before(func() {
				h.AssertNil(t, fakeOriginalImage.AddLayer(testLayerTarPath))
			})

			it("returns the layer's reader", func() {
				rc, err := subject.RetrieveLayer(testLayerSHA)
				h.AssertNil(t, err)
				defer rc.Close()

				bytes, err := ioutil.ReadAll(rc)
				h.AssertNil(t, err)
				h.AssertEq(t, string(bytes), "dummy data")
			})
		})

		when("layer does not exist", func() {
			it("returns an error", func() {
				_, err := subject.RetrieveLayer("some_nonexistent_sha")
				h.AssertError(t, err, "failed to get layer with sha 'some_nonexistent_sha'")
			})
		})
	})

	when("#Commit", func() {
		when("with #SetMetadata", func() {
			var newMetadata platform.CacheMetadata

			it.Before(func() {
				h.AssertNil(t, fakeOriginalImage.SetLabel("io.buildpacks.lifecycle.cache.metadata", `{"buildpacks": [{"key": "old.bp.id"}]}`))

				newMetadata = platform.CacheMetadata{
					Buildpacks: []platform.BuildpackLayersMetadata{{
						ID: "new.bp.id",
					}},
				}
			})

			when("set then commit", func() {
				it("retrieve returns the newly set metadata", func() {
					h.AssertNil(t, subject.SetMetadata(newMetadata))

					err := subject.Commit()
					h.AssertNil(t, err)

					retrievedMetadata, err := subject.RetrieveMetadata()
					h.AssertNil(t, err)
					h.AssertEq(t, retrievedMetadata, newMetadata)
				})
			})

			when("set after commit", func() {
				it("retrieve returns the newly set metadata", func() {
					err := subject.Commit()
					h.AssertNil(t, err)

					h.AssertError(t, subject.SetMetadata(newMetadata), "cache cannot be modified after commit")
				})
			})

			when("set without commit", func() {
				it("retrieve returns the previous metadata", func() {
					previousMetadata := platform.CacheMetadata{
						Buildpacks: []platform.BuildpackLayersMetadata{{
							ID: "old.bp.id",
						}},
					}

					h.AssertNil(t, subject.SetMetadata(newMetadata))

					retrievedMetadata, err := subject.RetrieveMetadata()
					h.AssertNil(t, err)
					h.AssertEq(t, retrievedMetadata, previousMetadata)
				})
			})
		})

		when("with #AddLayerFile", func() {
			when("add then commit", func() {
				it("retrieve returns newly added layer", func() {
					h.AssertNil(t, subject.AddLayerFile(testLayerTarPath, testLayerSHA))

					err := subject.Commit()
					h.AssertNil(t, err)

					rc, err := subject.RetrieveLayer(testLayerSHA)
					h.AssertNil(t, err)
					defer rc.Close()

					bytes, err := ioutil.ReadAll(rc)
					h.AssertNil(t, err)
					h.AssertEq(t, string(bytes), "dummy data")
				})
			})

			when("add after commit", func() {
				it("retrieve returns the newly set metadata", func() {
					err := subject.Commit()
					h.AssertNil(t, err)

					h.AssertError(t, subject.AddLayerFile(testLayerTarPath, testLayerSHA), "cache cannot be modified after commit")
				})
			})

			when("add without commit", func() {
				it("retrieve returns not found error", func() {
					h.AssertNil(t, subject.AddLayerFile(testLayerTarPath, testLayerSHA))

					_, err := subject.RetrieveLayer(testLayerSHA)
					h.AssertError(t, err, fmt.Sprintf("failed to get layer with sha '%s'", testLayerSHA))
				})
			})
		})

		when("with #ReuseLayer", func() {
			it.Before(func() {
				fakeNewImage.AddPreviousLayer(testLayerSHA, testLayerTarPath)
				h.AssertNil(t, fakeOriginalImage.AddLayer(testLayerTarPath))
			})

			when("reuse then commit", func() {
				it("returns the reused layer", func() {
					h.AssertNil(t, subject.ReuseLayer(testLayerSHA))

					err := subject.Commit()
					h.AssertNil(t, err)

					rc, err := subject.RetrieveLayer(testLayerSHA)
					h.AssertNil(t, err)
					defer rc.Close()

					bytes, err := ioutil.ReadAll(rc)
					h.AssertNil(t, err)
					h.AssertEq(t, string(bytes), "dummy data")
				})
			})

			when("reuse after commit", func() {
				it("retrieve returns the newly set metadata", func() {
					err := subject.Commit()
					h.AssertNil(t, err)

					h.AssertError(t, subject.ReuseLayer(testLayerSHA), "cache cannot be modified after commit")
				})
			})

			when("reuse without commit", func() {
				it("retrieve returns the previous layer", func() {
					h.AssertNil(t, subject.ReuseLayer(testLayerSHA))

					rc, err := subject.RetrieveLayer(testLayerSHA)
					h.AssertNil(t, err)
					defer rc.Close()

					bytes, err := ioutil.ReadAll(rc)
					h.AssertNil(t, err)
					h.AssertEq(t, string(bytes), "dummy data")
				})
			})
		})

		when("attempting to commit more than once", func() {
			it("should fail", func() {
				err := subject.Commit()
				h.AssertNil(t, err)

				err = subject.Commit()
				h.AssertError(t, err, "cache cannot be modified after commit")
			})
		})

		when("with #DeleteOrigImage", func() {
			when("original and new image are different", func() {
				it.Before(func() {
					fakeOriginalImage = fakes.NewImage("fake-image", "", local.IDIdentifier{ImageID: "fakeOrigImage"})
					fakeNewImage = fakes.NewImage("fake-image", "", local.IDIdentifier{ImageID: "fakeNewImage"})
					subject = cache.NewImageCache(fakeOriginalImage, fakeNewImage)
				})
				it("should delete original image", func() {
					err := subject.DeleteOrigImage()
					h.AssertNil(t, err)
					h.AssertEq(t, fakeOriginalImage.Found(), false)
				})
			})

			when("original and new image are the same", func() {
				it.Before(func() {
					fakeOriginalImage = fakes.NewImage("fake-image", "", local.IDIdentifier{ImageID: "fakeOrigImage"})
					fakeNewImage = fakes.NewImage("fake-image", "", local.IDIdentifier{ImageID: "fakeOrigImage"})
					subject = cache.NewImageCache(fakeOriginalImage, fakeNewImage)
				})
				it("should not delete original image", func() {
					err := subject.DeleteOrigImage()
					h.AssertNil(t, err)
					h.AssertEq(t, fakeOriginalImage.Found(), true)
				})
			})
		})
	})
}
