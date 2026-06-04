package store

import (
	"os"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
)

type SimulationStore struct{ io *IO }

func NewSimulationStore(io *IO) *SimulationStore { return &SimulationStore{io: io} }

func (s *SimulationStore) Load() (*domain.SimulationProfile, error) {
	var profile domain.SimulationProfile
	if err := s.io.ReadJSON("meta/simulation_profile.json", &profile); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := domain.ValidateSimulationProfile(&profile); err != nil {
		return nil, err
	}
	return &profile, nil
}

func (s *SimulationStore) Save(profile domain.SimulationProfile) error {
	if profile.Version == "" {
		profile.Version = domain.SimulationProfileVersion
	}
	if err := domain.ValidateSimulationProfile(&profile); err != nil {
		return err
	}
	return s.io.WriteJSON("meta/simulation_profile.json", profile)
}
