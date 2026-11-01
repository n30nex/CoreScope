# Cross-border map geometry

These datasets support approximate map display and classification of reported
radio positions. They are not surveyed device positions or a legal boundary
determination. A repeater's current reported position may differ from its
position when an older packet was recorded.

## Country polygons

`countries.geojson` contains Canada and United States ADM0 polygons from
geoBoundaries, pinned to commit
`9469f09592ced973a3448cf66b6100b741b64c0d`:

- [Canada](https://github.com/wmgeolab/geoBoundaries/raw/9469f09592ced973a3448cf66b6100b741b64c0d/releaseData/gbOpen/CAN/ADM0/geoBoundaries-CAN-ADM0_simplified.geojson),
  boundary `CAN-ADM0-50000805`, representing 2020. Source: OpenStreetMap /
  Wambacher; [Open Database Licence 1.0](https://opendatacommons.org/licenses/odbl/1-0/),
  [OpenStreetMap attribution](https://www.openstreetmap.org/copyright).
- [United States](https://github.com/wmgeolab/geoBoundaries/raw/9469f09592ced973a3448cf66b6100b741b64c0d/releaseData/gbOpen/USA/ADM0/geoBoundaries-USA-ADM0_simplified.geojson),
  boundary `USA-ADM0-2327393`, representing 2018. Source: United States Census
  Bureau, MAF/TIGER Database; public domain.

Preparation: keep geometry and country code only; apply Shapely
`simplify(0.001, preserve_topology=True)` in the original longitude/latitude
coordinates, then serialize compact JSON. Both resulting geometries were
checked valid. The tolerance is in degrees, not metres; it is not a claim of
survey accuracy. Country polygons remain a separately licensed data asset.

Please cite [geoBoundaries](https://www.geoboundaries.org/), Runfola et al.
(2020), and the underlying sources when reusing these polygons.

## International border

`border.geojson` preserves the line coordinates from **International Boundary
between Canada and the United States, version 1.3**, published by the
International Boundary Commission. Only the file format and unused attributes
were changed; the line coordinates were not simplified.

- [Source and mapping limitations](https://internationalboundarycommission.org/en/maps-coordinates/coordinates.php)
- [Original shapefile archive](https://internationalboundarycommission.org/uploads/shapefile/us-canada-boundary-v1-3.zip)
- Coordinates: NAD83 geographic longitude/latitude, GRS80 ellipsoid.
- The archive's Canadian and United States metadata explicitly license use
  under the [Open Government Licence - Canada](https://open.canada.ca/en/open-government-licence-canada).
- Contains information licensed under the Open Government Licence - Canada.
  Copyright International Boundary Commission. This adaptation is not
  affiliated with or endorsed by the Commission.

The Commission describes these coordinates as for mapping use only. Distances
shown in CoreScope are approximate distances to this mapped international
border, not to the shoreline or a MeshMapper region outline.
