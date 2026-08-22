// Package predict performs inference using assembled feature vector to get
// probability of rain.
//
// The booster is read from LightGBM's native text format (model.txt) by
// github.com/dmitryikh/leaves, a pure-Go parser.
//
// Model file is read at start up, so the latest @production model is used by
// every instance. No code changes when a new model is promoted. To serve the
// updated model, backend service is simply restarted (i.e. instances rotated).
package predict
