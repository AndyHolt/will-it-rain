// Package features assembles the model's input row from a forecast.
//
// serving.json names the columns the model was fitted on, in order, so model
// features are defined as data rather than code: a retrain that adds or drops a
// forecast variable needs no change here.
//
// *Classes* of feature are encoded: a base column, a lagged copy of one, and
// the two seasonal ones. So a new feature class will require code changes.
package features
